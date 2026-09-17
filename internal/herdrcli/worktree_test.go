package herdrcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// worktreeStub writes a fake host binary that records argv and answers every
// worktree command with a plausible envelope.
func worktreeStub(t *testing.T) (*Client, func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "argv.log")
	path := filepath.Join(dir, "bora")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n" +
		`case "$*" in
  *"worktree list"*) echo '{"id":"cli:worktree:list","result":{"worktrees":[],"source":{}}}' ;;
  *) echo '{"id":"cli:worktree","result":{"workspace":{"workspace_id":"w1"},"tab":{},"root_pane":{},"worktree":{}}}' ;;
esac` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{Bin: path}, func() []string {
		raw, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
}

// exerciseWorktreeCommands runs every command that used to carry the flag.
func exerciseWorktreeCommands(t *testing.T, client *Client) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := client.Worktrees(ctx, t.TempDir()); err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if _, err := client.CreateWorktree(ctx, t.TempDir(), "feature/FTR-0001/x", "main", "label", false); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	if _, err := client.OpenWorktree(ctx, t.TempDir(), "feature/FTR-0001/x", "", "label", false); err != nil {
		t.Fatalf("worktree open: %v", err)
	}
	if err := client.RemoveWorktree(ctx, "w1", false); err != nil {
		t.Fatalf("worktree remove: %v", err)
	}
}

// VB-08. `--trust-repository` skips the host's own repository-trust gate. It
// used to be appended to all four worktree commands unconditionally, which
// answered that gate on the operator's behalf, silently, for every repository
// hvb was ever pointed at — the same class of decision the README is proud of
// never taking with a harness's own trust dialog.
func TestWorktreeCommandsLeaveTheTrustGateAlone(t *testing.T) {
	t.Setenv(TrustRepositoryEnv, "")
	client, argv := worktreeStub(t)
	exerciseWorktreeCommands(t, client)

	lines := argv()
	if len(lines) != 4 {
		t.Fatalf("expected four worktree invocations, got %d: %v", len(lines), lines)
	}
	for _, line := range lines {
		if strings.Contains(line, "--trust-repository") {
			t.Errorf("hvb answered the trust gate by itself: %q", line)
		}
	}
}

// The operator can still opt out of the gate, in their own environment, which
// is not somewhere a repository can write.
func TestTrustGateIsSkippedOnlyWhenTheOperatorSaysSo(t *testing.T) {
	for _, value := range []string{"1", "true", "yes", "ON"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(TrustRepositoryEnv, value)
			client, argv := worktreeStub(t)
			exerciseWorktreeCommands(t, client)
			for _, line := range argv() {
				if !strings.Contains(line, "--trust-repository") {
					t.Errorf("the operator asked for it and %q went without: %q", value, line)
				}
			}
		})
	}
	for _, value := range []string{"", "0", "false", "no", "maybe", " "} {
		t.Run("refused/"+value, func(t *testing.T) {
			t.Setenv(TrustRepositoryEnv, value)
			if flags := trustFlags(); len(flags) != 0 {
				t.Errorf("%q must not read as consent, got %v", value, flags)
			}
		})
	}
}
