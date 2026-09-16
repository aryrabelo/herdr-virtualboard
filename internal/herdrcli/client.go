// Package herdrcli wraps the `herdr` CLI.
//
// hvb drives Herdr through its CLI rather than its unix socket. The CLI is the
// contract Herdr documents for external callers, it already emits the JSON
// envelopes this package decodes, and hvb needs no event subscription — the
// board is reconciled from `vb` and from polled pane state, so there is nothing
// a long-lived socket would buy that is worth owning a second protocol for.
//
// Every argv in this file was verified against `herdr <group>` usage output on
// Herdr 0.9.0 / socket protocol 22. Do not change one from memory: run the bare
// group command (`herdr pane`, `herdr agent`) and read the usage it prints.
package herdrcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Binary is the executable looked up when Client.Bin is empty. Herdr injects
// HERDR_BIN_PATH into the panes it manages, so a plugin running inside Herdr
// uses the exact binary that spawned it.
const Binary = "herdr"

// SupportedVersion and SupportedProtocol are the exact Herdr build hvb speaks
// to. Like herdr-board, this is a policy gate rather than a negotiation: a
// different protocol may have moved a field this package reads positionally out
// of a JSON result, and failing the dispatch is better than launching an agent
// into a pane hvb can no longer track.
const (
	SupportedVersion  = "0.9.0"
	SupportedProtocol = 22
)

// DefaultTimeout bounds a single herdr invocation that is not expected to wait
// on an agent.
const DefaultTimeout = 30 * time.Second

// Client runs herdr CLI commands.
type Client struct {
	// Bin is the herdr executable; empty resolves HERDR_BIN_PATH then PATH.
	Bin string
	// Session targets a named Herdr session. Empty uses the default session,
	// which is what a plugin pane inherits.
	Session string
	// Timeout bounds one invocation. Zero means DefaultTimeout.
	Timeout time.Duration
}

// New builds a client using the ambient Herdr environment.
func New() *Client { return &Client{Bin: os.Getenv("HERDR_BIN_PATH")} }

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return Binary
}

// Error is a failed herdr invocation.
type Error struct {
	Args    []string
	Code    int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("herdr %s: %s (exit %d)", strings.Join(e.Args, " "), e.Message, e.Code)
}

// ErrIncompatible is returned by Gate when the running Herdr is not the exact
// supported version and protocol.
var ErrIncompatible = errors.New("unsupported herdr version")

// Status is the decoded `herdr status` report. The command prints YAML-ish
// blocks rather than JSON, so this is parsed line-wise.
type Status struct {
	ClientVersion  string `json:"client_version"`
	ClientProtocol int    `json:"client_protocol"`
	ServerVersion  string `json:"server_version"`
	ServerRunning  bool   `json:"server_running"`
	Socket         string `json:"socket"`
}

// Status reads `herdr status`.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	out, err := c.raw(ctx, "status")
	if err != nil {
		return nil, err
	}
	status := &Status{}
	section := ""
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			section = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch section + "." + key {
		case "client.version":
			status.ClientVersion = value
		case "client.protocol":
			status.ClientProtocol = atoi(value)
		case "server.version":
			status.ServerVersion = value
		case "server.status":
			status.ServerRunning = value == "running"
		case "server.socket":
			status.Socket = value
		}
	}
	return status, nil
}

// Gate enforces the supported-version policy. Callers run it before any
// operation that creates layout or launches an agent.
//
// The one deliberate exception, mirroring herdr-board, is cleanup and liveness
// for panes hvb already owns: CloseP{ane} and pane reads stay ungated so a Herdr
// upgrade cannot strand a run hvb is responsible for tidying up.
func (c *Client) Gate(ctx context.Context) error {
	status, err := c.Status(ctx)
	if err != nil {
		return fmt.Errorf("%w: cannot read herdr status: %v", ErrIncompatible, err)
	}
	if !status.ServerRunning {
		return fmt.Errorf("%w: herdr server is not running", ErrIncompatible)
	}
	if status.ServerVersion != SupportedVersion {
		return fmt.Errorf("%w: need herdr %s, found %s", ErrIncompatible, SupportedVersion, status.ServerVersion)
	}
	if status.ClientProtocol != SupportedProtocol {
		return fmt.Errorf("%w: need socket protocol %d, found %d", ErrIncompatible, SupportedProtocol, status.ClientProtocol)
	}
	return nil
}

// InHerdr reports whether the current process runs inside a Herdr-managed pane.
// Herdr sets HERDR_ENV=1 there; its own agent skill makes this the precondition
// for any control command.
func InHerdr() bool { return os.Getenv("HERDR_ENV") == "1" }

// Available reports whether the herdr executable can be found.
func (c *Client) Available() error {
	if _, err := exec.LookPath(c.bin()); err != nil {
		return fmt.Errorf("herdr CLI not found on PATH: %w", err)
	}
	return nil
}

// envelope is the shape every JSON-returning herdr command uses:
// `{"id":"cli:pane:list","result":{...}}`, or an `error` member on failure.
type envelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"error"`
}

// call runs a herdr command and unmarshals `.result` into out. Passing a nil
// out runs the command for its effect and only checks the envelope.
func (c *Client) call(ctx context.Context, out any, args ...string) error {
	raw, err := c.raw(ctx, args...)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("herdr %s: decode response: %w", strings.Join(args, " "), err)
	}
	if env.Error != nil {
		return &Error{Args: args, Code: env.Error.Code, Message: env.Error.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("herdr %s: decode result: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (c *Client) raw(ctx context.Context, args ...string) ([]byte, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.rawNoTimeout(ctx, args...)
}

// rawNoTimeout runs herdr with only the caller's context bounding it. Agent
// waits carry their own `--timeout` in milliseconds and must not be cut short
// by the client-side default.
func (c *Client) rawNoTimeout(ctx context.Context, args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+2)
	if c.Session != "" {
		full = append(full, "--session", c.Session)
	}
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, c.bin(), full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, &Error{Args: full, Code: -1, Message: "timed out"}
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = strings.TrimSpace(stdout.String())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A failed herdr command still prints its JSON error envelope on
		// stdout; prefer that message over the raw exit status.
		var env envelope
		if json.Unmarshal(stdout.Bytes(), &env) == nil && env.Error != nil {
			return nil, &Error{Args: full, Code: env.Error.Code, Message: env.Error.Message}
		}
		return nil, &Error{Args: full, Code: exitErr.ExitCode(), Message: message}
	}
	return nil, fmt.Errorf("run herdr %s: %w", strings.Join(full, " "), err)
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
