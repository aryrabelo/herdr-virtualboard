package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// An agent shelling out to `hvb run show` reconciles the board on the way past.
// It must never park its own run: it is the evidence that the run is alive, and
// Herdr can legitimately report the pane as `done` between turns.
func TestReconcileNeverParksTheCallersOwnRun(t *testing.T) {
	script := `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[{"pane_id":"w1:p1","agent":"claude","agent_status":"done"}]}}' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
	h.addRun("self", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	h.addRun("other", "FTR-0001", feature.InProgress, runs.Running, "w1:p2")
	h.dispatcher.SelfRunID = "self"

	if _, err := h.dispatcher.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	self, err := h.dispatcher.Store.Get("self")
	if err != nil {
		t.Fatal(err)
	}
	if self.State != runs.Running {
		t.Fatalf("the caller's own run was parked as %q", self.State)
	}
	// Every other run is still reconciled normally.
	other, err := h.dispatcher.Store.Get("other")
	if err != nil {
		t.Fatal(err)
	}
	if other.State != runs.Awaiting {
		t.Fatalf("another run with a vanished pane = %q, want awaiting", other.State)
	}
}

// A fresh worktree is a directory the harness has never seen, so Claude Code
// stops at its trust prompt and `agent start` answers agent_not_ready. The
// agent is alive; treating that as a failed launch would break every first
// worktree dispatch.
func TestBlockedStartupParksTheTaskInsteadOfFailing(t *testing.T) {
	script := `case "$*" in
  *"agent start"*)
    echo '{"error":{"code":"agent_not_ready","message":"agent x is blocked during startup and is not ready for prompts"},"id":"cli:agent:start"}' >&2
    exit 1 ;;
  *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p3","tab_id":"w1:t2","workspace_id":"w1"}]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[]}}' ;;
  *"tab list"*) echo '{"id":"x","result":{"tabs":[]}}' ;;
  *"tab create"*) echo '{"id":"x","result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}' ;;
  *"pane split"*) echo '{"id":"x","result":{"pane":{"pane_id":"w1:p3"}}}' ;;
  *"workspace create"*) echo '{"id":"x","result":{"workspace":{"workspace_id":"w2"},"tab":{"tab_id":"w2:t1"},"root_pane":{"pane_id":"w2:p1"}}}' ;;
  *"workspace list"*) echo '{"id":"x","result":{"workspaces":[{"workspace_id":"w1","visual_group":null}]}}' ;;
  *status*) printf 'client:\n  version: 0.48.0\n  protocol: 25\n\nserver:\n  status: running\n  version: 0.48.0\n  socket: /tmp/s\n' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
	spec, err := h.dispatcher.Workspace.FindSpec("FTR-0001")
	if err != nil {
		t.Fatal(err)
	}

	run, err := h.dispatcher.Start(context.Background(), Request{Spec: spec})
	if err != nil {
		t.Fatalf("a blocked startup must not fail the dispatch: %v", err)
	}
	if run.State != runs.Running {
		t.Errorf("State = %q, want running — the agent is alive", run.State)
	}
	if run.PendingPrompt == "" {
		t.Fatal("the task should be parked for submission once the agent settles")
	}
	// What is parked is the pointer, the same string the live path submits,
	// so the deferred submission cannot reintroduce the collapsed paste.
	// The task itself is in the file the pointer names.
	path, found := promptPathIn(run.PendingPrompt)
	if !found {
		t.Fatalf("the parked prompt names no prompt file:\n%s", run.PendingPrompt)
	}
	if len(run.PendingPrompt) > promptPointerBudget {
		t.Errorf("the parked prompt is %d bytes, over the %d-byte pointer budget",
			len(run.PendingPrompt), promptPointerBudget)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the parked prompt points at a file that is not there: %v", err)
	}
	if !strings.Contains(string(body), "FTR-0001") {
		t.Error("the parked prompt should point at the real task")
	}
}

// promptPathIn picks the prompt file path out of a pointer prompt, where it
// sits alone on its own line.
func promptPathIn(pointer string) (string, bool) {
	for _, line := range strings.Split(pointer, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".md") && filepath.IsAbs(line) {
			return line, true
		}
	}
	return "", false
}

// Once the user answers the dialog, the parked task goes in by itself.
func TestReconcileSubmitsTheParkedTaskWhenTheAgentSettles(t *testing.T) {
	script := `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p3","tab_id":"w1:t2","workspace_id":"w1"}]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[{"pane_id":"w1:p3","agent":"claude","agent_status":"idle"}]}}' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
	run := h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p3")
	if _, err := h.dispatcher.Store.Update(run.ID, func(r *runs.Run) error {
		r.AgentName, r.PendingPrompt = "ftr-0001-a", "do the work"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.dispatcher.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, err := h.dispatcher.Store.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.PendingPrompt != "" {
		t.Fatal("the parked task should have been submitted once the agent went idle")
	}
	if updated.State != runs.Running {
		t.Errorf("State = %q, want the run still running", updated.State)
	}
}

// A run still sitting at the dialog must not be mistaken for one that finished.
func TestReconcileLeavesAStillBlockedRunParked(t *testing.T) {
	script := `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p3","tab_id":"w1:t2","workspace_id":"w1"}]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[{"pane_id":"w1:p3","agent":"claude","agent_status":"blocked"}]}}' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
	run := h.addRun("r1", "FTR-0001", feature.InProgress, runs.Running, "w1:p3")
	if _, err := h.dispatcher.Store.Update(run.ID, func(r *runs.Run) error {
		r.PendingPrompt = "do the work"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dispatcher.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, _ := h.dispatcher.Store.Get("r1")
	if updated.PendingPrompt == "" {
		t.Fatal("a blocked agent must keep its parked task")
	}
	if updated.State != runs.Running {
		t.Errorf("State = %q", updated.State)
	}
}
