// Package herdrcli wraps the `herdr` CLI.
//
// hvb drives Herdr through its CLI rather than its unix socket. The CLI is the
// contract Herdr documents for external callers, it already emits the JSON
// envelopes this package decodes, and hvb needs no event subscription — the
// board is reconciled from `vb` and from polled pane state, so there is nothing
// a long-lived socket would buy that is worth owning a second protocol for.
//
// Every argv in this file was verified against `bora <group> --help` usage
// output on bora 0.48.0 / socket protocol 25, and against Herdr 0.9.0 upstream
// before that. Do not change one from memory: run the group command
// (`bora pane --help`, `bora agent --help`) and read the usage it prints.
package herdrcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Binary is the executable looked up when Client.Bin is empty. Herdr injects
// HERDR_BIN_PATH into the panes it manages, so a plugin running inside Herdr
// uses the exact binary that spawned it. The fallback is this fork's
// distribution name, `bora`; see FORK.md.
const Binary = "bora"

// DefaultMinVersion and DefaultMinProtocol are the compatibility FLOOR hvb
// speaks to, not the one exact build it speaks to.
//
// Upstream pinned an exact version and protocol, reasoning that a different
// protocol may have moved a field this package reads out of a JSON result, and
// that failing the dispatch beats launching an agent into a pane hvb can no
// longer track. That intent is kept. What changes is "exactly this build"
// becoming "this build or newer": the host ships releases far faster than this
// plugin, and an exact pin fails on every release that moved nothing hvb reads.
//
// The protocol floor is 25 because that is the protocol whose CLI surface was
// verified argv-by-argv against this package. Upstream Herdr 0.9.0 speaks
// protocol 22; run against it with HVB_MIN_HERDR_PROTOCOL=22.
const (
	DefaultMinVersion  = "0.9.0"
	DefaultMinProtocol = 25
)

// EnvMinVersion and EnvMinProtocol lower or raise the floor from the
// environment, so an operator on a host this build has never seen can unblock
// themselves without recompiling hvb.
const (
	EnvMinVersion  = "HVB_MIN_HERDR_VERSION"
	EnvMinProtocol = "HVB_MIN_HERDR_PROTOCOL"
)

// DefaultTimeout bounds a single herdr invocation that is not expected to wait
// on an agent.
const DefaultTimeout = 30 * time.Second

// EnvBinPath names the host executable. The host injects it into every pane it
// manages, so a plugin running inside the host uses the exact binary that
// spawned it.
const EnvBinPath = "HERDR_BIN_PATH"

// Client runs host CLI commands.
type Client struct {
	// Bin is the host executable. Empty falls back to Binary on PATH.
	Bin string
	// Session targets a named host session. Empty uses the default session,
	// which is what a plugin pane inherits.
	Session string
	// Timeout bounds one invocation. Zero means DefaultTimeout.
	Timeout time.Duration
}

// New builds a client using the ambient host environment.
func New() *Client { return &Client{Bin: TrustedBinPath(os.Getenv(EnvBinPath))} }

// TrustedBinPath accepts an HERDR_BIN_PATH value only when it names an
// absolute path to a regular executable file, and returns "" otherwise so the
// caller falls back to Binary on PATH.
//
// This value is re-executed with no argument review, and hvb inherits it from
// an environment a dispatched agent also writes to. A bare name or a relative
// path would resolve against PATH or against the process cwd — which during a
// dispatch is a repository an agent has just been writing files into, so
// dropping a `bora` there would be enough to be run. Requiring an absolute
// path to something already executable removes that whole class of hijack
// without needing to know what the legitimate path is.
//
// Rejection is silent, unlike a malformed version floor: the floor is operator
// intent worth an error, while this variable is host-injected, and the safe
// response to a value that does not look host-injected is to ignore it.
func TrustedBinPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !filepath.IsAbs(raw) {
		return ""
	}
	info, err := os.Stat(raw)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return raw
}

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

// ErrIncompatible is returned by Gate when the running Herdr is older than the
// compatibility floor, or when the floor itself is misconfigured.
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

// Floor is the compatibility floor Gate enforces.
type Floor struct {
	Version  string
	Protocol int
}

// ResolvedFloor is the floor actually in effect: the compiled defaults with the
// operator's environment overrides applied.
//
// A malformed override is an error, not a silent fallback. An operator who
// exported the variable is entitled to know it did not take, rather than
// discovering later that the floor they thought they lowered was still in
// force.
func ResolvedFloor() (Floor, error) {
	floor := Floor{Version: DefaultMinVersion, Protocol: DefaultMinProtocol}
	if raw := strings.TrimSpace(os.Getenv(EnvMinVersion)); raw != "" {
		if _, ok := parseVersion(raw); !ok {
			return floor, fmt.Errorf("%s=%q is not a semantic version", EnvMinVersion, raw)
		}
		floor.Version = raw
	}
	if raw := strings.TrimSpace(os.Getenv(EnvMinProtocol)); raw != "" {
		protocol, err := strconv.Atoi(raw)
		if err != nil || protocol < 0 {
			return floor, fmt.Errorf("%s=%q is not a protocol number", EnvMinProtocol, raw)
		}
		floor.Protocol = protocol
	}
	return floor, nil
}

// version is a three-field semantic version.
//
// Versions must never be compared as strings. "0.48.0" sorts BELOW "0.9.0"
// lexicographically, because '4' < '9', so a string floor rejects every host
// release past 0.9 — which is the exact breakage this fork exists to fix.
type version struct{ major, minor, patch int }

// parseVersion reads major[.minor[.patch]], tolerating a leading `v` and
// discarding prerelease or build metadata (`0.48.0-rc1+abc`).
func parseVersion(raw string) (version, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if cut := strings.IndexAny(raw, "-+"); cut >= 0 {
		raw = raw[:cut]
	}
	fields := strings.Split(raw, ".")
	if raw == "" || len(fields) > 3 {
		return version{}, false
	}
	var parsed version
	into := [...]*int{&parsed.major, &parsed.minor, &parsed.patch}
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return version{}, false
		}
		*into[i] = n
	}
	return parsed, true
}

// less orders two versions field by field, most significant first.
func (v version) less(other version) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

// Gate enforces the compatibility floor. Callers run it before any operation
// that creates layout or launches an agent.
//
// The one deliberate exception, mirroring herdr-board, is cleanup and liveness
// for panes hvb already owns: CloseP{ane} and pane reads stay ungated so a Herdr
// upgrade cannot strand a run hvb is responsible for tidying up.
func (c *Client) Gate(ctx context.Context) error {
	floor, err := ResolvedFloor()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIncompatible, err)
	}
	required, ok := parseVersion(floor.Version)
	if !ok {
		return fmt.Errorf("%w: floor %q is not a semantic version", ErrIncompatible, floor.Version)
	}
	status, err := c.Status(ctx)
	if err != nil {
		return fmt.Errorf("%w: cannot read herdr status: %v", ErrIncompatible, err)
	}
	if !status.ServerRunning {
		return fmt.Errorf("%w: herdr server is not running", ErrIncompatible)
	}
	found, ok := parseVersion(status.ServerVersion)
	if !ok {
		return fmt.Errorf("%w: cannot read herdr version %q", ErrIncompatible, status.ServerVersion)
	}
	if found.less(required) {
		return fmt.Errorf("%w: need herdr %s or newer, found %s", ErrIncompatible, floor.Version, status.ServerVersion)
	}
	if status.ClientProtocol < floor.Protocol {
		return fmt.Errorf("%w: need socket protocol %d or newer, found %d", ErrIncompatible, floor.Protocol, status.ClientProtocol)
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
