package ghboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// runner invokes the GitHub CLI and returns its stdout. It is the single seam
// between this package and the outside world, so tests replace it with a
// fixture reader and never touch the network.
//
// Implementations must honour ctx: Load is called from the TUI's refresh loop,
// which cancels in-flight work when the user quits.
type runner func(ctx context.Context, args ...string) ([]byte, error)

// ghBinary is the CLI we shell out to. Authentication is the binary's problem:
// it reads its own keyring/GH_TOKEN, so no credential ever passes through this
// process and none can leak into a log line here.
const ghBinary = "gh"

// execRunner is the production runner.
func execRunner(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath(ghBinary)
	if err != nil {
		return nil, fmt.Errorf("the GitHub CLI (%s) is not in PATH: install it and run `gh auth login`: %w", ghBinary, err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// gh prompts when it thinks it has a terminal; a nil Stdin guarantees it
	// cannot block this call waiting for input it will never get.
	cmd.Stdin = nil

	if err := cmd.Run(); err != nil {
		return nil, ghError(ctx, err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

// ghError turns a failed gh invocation into a diagnosis the board can show. gh
// writes its reason to stderr ("not logged in", "Could not resolve to a
// Repository", "dial tcp: no such host"), and that text is the useful part; the
// exit status alone is not. gh never echoes the token on stderr, and this
// package never asks for it, so nothing secret can reach the returned error.
func ghError(ctx context.Context, runErr error, stderr []byte) error {
	reason := strings.TrimSpace(string(stderr))
	if reason == "" {
		reason = runErr.Error()
	}
	if len(reason) > maxStderrBytes {
		reason = reason[:maxStderrBytes] + "…"
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("gh was interrupted (%w): %s", ctxErr, reason)
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return fmt.Errorf("gh exited %d: %s", exitErr.ExitCode(), reason)
	}
	return fmt.Errorf("gh could not be run: %s", reason)
}

// maxStderrBytes caps how much of gh's stderr reaches the board. A GraphQL
// error body can run for kilobytes; the first line carries the diagnosis.
const maxStderrBytes = 512
