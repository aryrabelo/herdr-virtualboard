package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// liveHerdr answers every call a full Start makes, so these tests run the
// whole dispatch — the run's own workspace, `agent start`, the prompt —
// against a stub binary that records argv. That is the only place the harness
// kind is observable as a fact rather than as the resolver's own opinion.
//
// The tab and split answers are the worktree path's; a plain run goes through
// `workspace create`.
const liveHerdr = `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[{"pane_id":"w1:p3","tab_id":"w1:t2","workspace_id":"w1"}]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[]}}' ;;
  *"tab list"*) echo '{"id":"x","result":{"tabs":[]}}' ;;
  *"tab create"*) echo '{"id":"x","result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}' ;;
  *"pane split"*) echo '{"id":"x","result":{"pane":{"pane_id":"w1:p3"}}}' ;;
  *"workspace create"*) echo '{"id":"x","result":{"workspace":{"workspace_id":"w2"},"tab":{"tab_id":"w2:t1"},"root_pane":{"pane_id":"w2:p1"}}}' ;;
  *"workspace list"*) echo '{"id":"x","result":{"workspaces":[{"workspace_id":"w1","visual_group":"cards"}]}}' ;;
  *status*) printf 'client:\n  version: 0.48.0\n  protocol: 25\n\nserver:\n  status: running\n  version: 0.48.0\n  socket: /tmp/s\n' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`

// withCharters writes real charter files into the workspace's agents
// directory and loads them the way hvb does, so Role.Path is set and the
// refusal can derive the directory to point at.
func withCharters(t *testing.T, h *harness, keys ...string) {
	t.Helper()
	dir := filepath.Join(h.root, workspace.Dir, "agents")
	for _, key := range keys {
		body := "---\nname: " + key + "\ndescription: " + key + " charter\n---\n\n# " + key + "\n"
		if err := os.WriteFile(filepath.Join(dir, key+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := roles.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h.dispatcher.Roles = loaded
}

// humanOnlySpec is the workspace's feature with the `hitl` label on it, which
// is how the owner's cards arrive: 6 of the 33 on his queue carry it.
func humanOnlySpec(t *testing.T, h *harness, id string) *feature.Spec {
	t.Helper()
	spec, err := h.dispatcher.Workspace.FindSpec(id)
	if err != nil {
		t.Fatal(err)
	}
	spec.Labels = []string{"hitl"}
	return spec
}

func agentStartLine(t *testing.T, argv []string) string {
	t.Helper()
	for _, line := range argv {
		if strings.HasPrefix(line, "agent start") {
			return line
		}
	}
	t.Fatalf("herdr was never asked to start an agent; it saw %v", argv)
	return ""
}

// A `hitl` card routes to the unblocker charter and runs under the unblocker
// harness. Both halves are read where they land — the run record and the
// `herdr agent start` command line — and not from the resolvers, because a
// resolver that agrees with itself is how the first version of this gate
// shipped green and inert.
func TestStartRoutesAHumanOnlyCardToTheUnblocker(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0160": feature.InProgress}, liveHerdr)
	withCharters(t, h, "destravador", "executor", "qa")

	run, err := h.dispatcher.Start(context.Background(), Request{Spec: humanOnlySpec(t, h, "FTR-0160")})
	if err != nil {
		t.Fatalf("a hitl card with an unblocker charter must dispatch: %v", err)
	}
	if run.Role != "destravador" {
		t.Errorf("Role = %q, want the unblocker charter", run.Role)
	}
	if run.Kind != h.dispatcher.Config.HumanOnlyHarness {
		t.Errorf("Kind = %q, want %q", run.Kind, h.dispatcher.Config.HumanOnlyHarness)
	}
	line := agentStartLine(t, h.herdrArgv())
	if !strings.Contains(line, "--kind "+h.dispatcher.Config.HumanOnlyHarness) {
		t.Errorf("herdr started %q, want --kind %s", line, h.dispatcher.Config.HumanOnlyHarness)
	}
}

// The explicit role loses to the routing, which is the door the old fallback
// would come back through: `--role executor` on a card whose whole content is
// something only the owner can do must still not reach an implementer.
func TestStartRoutesAHumanOnlyCardEvenWhenAnImplementerWasAskedFor(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0160": feature.InProgress}, liveHerdr)
	withCharters(t, h, "destravador", "executor", "qa")

	run, err := h.dispatcher.Start(context.Background(), Request{
		Spec: humanOnlySpec(t, h, "FTR-0160"), Role: "executor"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Role != "destravador" {
		t.Fatalf("Role = %q, want the unblocker charter — an explicit role must not beat the routing", run.Role)
	}
}

// An explicit --harness does win, and that is the one precedence the routing
// gives up: it names a program, not a charter, and silently ignoring the flag
// would make it a lie. The invariant the routing defends is the charter, and
// this shows it holds while the harness moves.
func TestAnExplicitHarnessStillWinsOnAHumanOnlyCard(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0160": feature.InProgress}, liveHerdr)
	withCharters(t, h, "destravador", "executor")

	run, err := h.dispatcher.Start(context.Background(), Request{
		Spec: humanOnlySpec(t, h, "FTR-0160"), Kind: "codex"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Kind != "codex" {
		t.Errorf("Kind = %q, want the harness the operator typed", run.Kind)
	}
	if run.Role != "destravador" {
		t.Errorf("Role = %q, want the unblocker charter regardless of the harness", run.Role)
	}
}

// A per-column harness must not decide for a `hitl` card. The Blocked column
// it lands in (fila.ColumnFor) also holds every card the frontier blocked, and
// stock configuration already sets per-column values with nobody typing a flag
// — config.Default gives the review column role = "qa".
func TestAColumnHarnessDoesNotOverrideTheUnblockerHarness(t *testing.T) {
	h := newHarness(t, map[string]feature.Status{"FTR-0160": feature.InProgress}, liveHerdr)
	withCharters(t, h, "destravador")
	column := h.dispatcher.Config.Column(feature.InProgress)
	column.Harness = "codex"
	h.dispatcher.Config.Columns[string(feature.InProgress)] = column

	run, err := h.dispatcher.Start(context.Background(), Request{Spec: humanOnlySpec(t, h, "FTR-0160")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Kind != h.dispatcher.Config.HumanOnlyHarness {
		t.Fatalf("Kind = %q, want %q — the column describes ordinary work, not this card",
			run.Kind, h.dispatcher.Config.HumanOnlyHarness)
	}
	// The control: the same column harness does decide for a card that is
	// not human-only, so the assertion above is the label's doing.
	ordinary, err := h.dispatcher.Workspace.FindSpec("FTR-0160")
	if err != nil {
		t.Fatal(err)
	}
	if kind := h.dispatcher.resolveKind(Request{Spec: ordinary}, column, false); kind != "codex" {
		t.Fatalf("an ordinary card resolved %q, want the column's codex", kind)
	}
}
