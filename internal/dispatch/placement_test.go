package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// placementHerdr is the fake host for the placement tests. It answers every
// call a full Start makes and, unlike the other stubs here, its answers move:
// the project root it reports a pane in, the sidebar group it reports that
// pane's workspace to be in, and whether grouping fails all come from the
// environment, so one script serves every fixture.
//
//	HVB_TEST_ROOT       the cwd `pane list` reports, matching the project
//	                    root or not
//	HVB_TEST_GROUP      the raw JSON value of `visual_group`, so a fixture
//	                    can be a name or `null`
//	HVB_TEST_SETGROUP   `fail` makes `workspace set-group` refuse
//	HVB_TEST_PROMPT_LOG where the whole `agent prompt` argv is captured,
//	                    which the shared argv log cannot hold because a
//	                    prompt spans lines
const placementHerdr = `case "$*" in
  *"workspace set-group"*)
    if [ "$HVB_TEST_SETGROUP" = "fail" ]; then
      echo '{"error":{"code":"invalid_request","message":"group refused"},"id":"cli:workspace:set-group"}' >&2
      exit 1
    fi
    echo '{"id":"x","result":{}}' ;;
  *"workspace create"*) echo '{"id":"x","result":{"workspace":{"workspace_id":"wRUN"},"tab":{"tab_id":"wRUN:t1"},"root_pane":{"pane_id":"wRUN:p1"}}}' ;;
  *"workspace list"*) printf '{"id":"x","result":{"workspaces":[{"workspace_id":"wHOST","label":"host","visual_group":%s}]}}\n' "$HVB_TEST_GROUP" ;;
  *"pane list"*) printf '{"id":"x","result":{"panes":[{"pane_id":"wHOST:p1","tab_id":"wHOST:t1","workspace_id":"wHOST","cwd":"%s"}]}}\n' "$HVB_TEST_ROOT" ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[]}}' ;;
  *"agent prompt"*) printf '%s' "$*" > "$HVB_TEST_PROMPT_LOG" ; echo '{"id":"x","result":{}}' ;;
  *"tab list"*) echo '{"id":"x","result":{"tabs":[]}}' ;;
  *"tab create"*) echo '{"id":"x","result":{"tab":{"tab_id":"wHOST:t2"},"root_pane":{"pane_id":"wHOST:p2"}}}' ;;
  *"pane split"*) echo '{"id":"x","result":{"pane":{"pane_id":"wHOST:p3"}}}' ;;
  *status*) printf 'client:\n  version: 0.48.0\n  protocol: 25\n\nserver:\n  status: running\n  version: 0.48.0\n  socket: /tmp/s\n' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`

// placed is a dispatcher whose fake host reports one workspace sitting in the
// project root, in the sidebar group given as raw JSON.
type placed struct {
	*harness
	promptLog string
}

func newPlaced(t *testing.T, groupJSON string) *placed {
	t.Helper()
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, placementHerdr)
	log := filepath.Join(t.TempDir(), "agent-prompt.argv")
	t.Setenv("HVB_TEST_ROOT", h.root)
	t.Setenv("HVB_TEST_GROUP", groupJSON)
	t.Setenv("HVB_TEST_SETGROUP", "ok")
	t.Setenv("HVB_TEST_PROMPT_LOG", log)
	return &placed{harness: h, promptLog: log}
}

func (p *placed) spec(t *testing.T) *feature.Spec {
	t.Helper()
	spec, err := p.dispatcher.Workspace.FindSpec("FTR-0001")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// start dispatches the fixture feature, failing the test if the dispatch is
// refused — for most of these tests "it still dispatched" is half the claim.
func (p *placed) start(t *testing.T) *runs.Run {
	t.Helper()
	run, err := p.dispatcher.Start(context.Background(), Request{Spec: p.spec(t)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return run
}

// hostCall returns the host command line beginning with prefix.
func hostCall(argv []string, prefix string) (string, bool) {
	for _, line := range argv {
		if strings.HasPrefix(line, prefix) {
			return line, true
		}
	}
	return "", false
}

// A plain run gets a workspace of its own and is never split into whatever tab
// the board happens to be running in. That is the defect the owner watched:
// his dispatch became a pane inside the board's own tab, because the launch
// split from an anchor there.
func TestAPlainRunOpensItsOwnWorkspaceInsteadOfSplittingTheCallersTab(t *testing.T) {
	p := newPlaced(t, `"bugtoprompt"`)
	run := p.start(t)
	argv := p.herdrArgv()

	create, ok := hostCall(argv, "workspace create")
	if !ok {
		t.Fatalf("no `workspace create` reached the host; it saw %v", argv)
	}
	label := runs.PaneLabel(run.FeatureID, run.Role)
	if !strings.Contains(create, "--label "+label) {
		t.Errorf("workspace opened as %q, want --label %s", create, label)
	}
	if !strings.Contains(create, "--cwd "+p.root) {
		t.Errorf("workspace opened outside the project root: %q", create)
	}
	if split, ok := hostCall(argv, "pane split"); ok {
		t.Errorf("the run was split into an existing tab: %q", split)
	}
	if run.WorkspaceID != "wRUN" || run.PaneID != "wRUN:p1" {
		t.Errorf("run placed at %s/%s, want the new workspace and its root pane",
			run.WorkspaceID, run.PaneID)
	}
	if run.AnchorPane != "" {
		t.Errorf("run.AnchorPane = %q, want none — a workspace root pane is split from nothing", run.AnchorPane)
	}
}

// The group is the project workspace's own, read from the host. Two fixtures
// with different names, because a constant in the code passes a single-value
// test and the board runs over whatever repository it was opened in.
func TestTheRunWorkspaceJoinsTheProjectWorkspacesGroup(t *testing.T) {
	for _, group := range []string{"bugtoprompt", "linha-de-producao"} {
		p := newPlaced(t, `"`+group+`"`)
		p.start(t)

		line, ok := hostCall(p.herdrArgv(), "workspace set-group")
		if !ok {
			t.Fatalf("group %q: the run workspace was never grouped; the host saw %v", group, p.herdrArgv())
		}
		if want := "workspace set-group wRUN " + group; line != want {
			t.Errorf("host got %q, want %q", line, want)
		}
	}
}

// A project workspace in no group leaves the run's workspace at the top level.
// Inventing a name would add a sidebar folder the owner never made.
func TestAProjectInNoGroupLeavesTheRunWorkspaceUngrouped(t *testing.T) {
	p := newPlaced(t, "null")
	run := p.start(t)

	if line, ok := hostCall(p.herdrArgv(), "workspace set-group"); ok {
		t.Errorf("hvb invented a group for a project that has none: %q", line)
	}
	if run.State != runs.Running {
		t.Errorf("State = %q, want running — an ungrouped workspace still runs", run.State)
	}
}

// The board can be running outside the project's own workspace, or the project
// can have no workspace open at all. There is then no group to copy, and the
// dispatch still happens.
func TestAProjectWithNoWorkspaceStillDispatches(t *testing.T) {
	p := newPlaced(t, `"bugtoprompt"`)
	t.Setenv("HVB_TEST_ROOT", t.TempDir())
	run := p.start(t)

	if line, ok := hostCall(p.herdrArgv(), "workspace set-group"); ok {
		t.Errorf("a group was applied from a workspace that is not the project's: %q", line)
	}
	if _, ok := hostCall(p.herdrArgv(), "workspace create"); !ok {
		t.Error("the run got no workspace of its own")
	}
	if run.State != runs.Running {
		t.Errorf("State = %q, want running", run.State)
	}
}

// Grouping is cosmetic and its failure is a note, never a refusal: a workspace
// in the wrong sidebar row is a nuisance, an abandoned dispatch is work that
// did not happen.
func TestAFailedGroupingDoesNotAbortTheDispatch(t *testing.T) {
	p := newPlaced(t, `"bugtoprompt"`)
	t.Setenv("HVB_TEST_SETGROUP", "fail")
	run := p.start(t)

	if run.State != runs.Running {
		t.Errorf("State = %q, want running", run.State)
	}
	if _, ok := hostCall(p.herdrArgv(), "agent start"); !ok {
		t.Error("the agent was never started")
	}
	if !strings.Contains(strings.Join(run.LastErrors, "\n"), "group") {
		t.Errorf("the failed grouping was swallowed instead of noted: %v", run.LastErrors)
	}
}

// The prompt travels as a path, not as text. Measured on the owner's session,
// ~11 KB in argv reached the agent as `[Paste #1, +222 lines]` with no body.
func TestTheAgentIsGivenThePromptFileAndNotThePromptBody(t *testing.T) {
	p := newPlaced(t, `"bugtoprompt"`)
	run := p.start(t)

	raw, err := os.ReadFile(p.promptLog)
	if err != nil {
		t.Fatalf("the host was never asked to prompt the agent: %v", err)
	}
	argv := string(raw)

	dir := filepath.Join(filepath.Dir(p.dispatcher.Store.Path()), "prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no prompt file was written: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%s holds %d files, want exactly the run's prompt", dir, len(entries))
	}
	path := filepath.Join(dir, entries[0].Name())
	if !strings.Contains(argv, path) {
		t.Errorf("the argv does not name the prompt file (%s):\n%s", path, argv)
	}
	// `agent prompt <name> ` plus the pointer, and nothing else.
	if budget := promptPointerBudget + 128; len(argv) > budget {
		t.Errorf("the `agent prompt` argv is %d bytes, over the %d-byte budget that keeps "+
			"it out of a collapsed paste:\n%s", len(argv), budget, argv)
	}
	if strings.Contains(argv, "untrusted-content") {
		t.Error("the prompt body travelled in argv, which is what the collapsed paste lost")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), run.FeatureID) {
		t.Error("the prompt file does not carry the feature it was written for")
	}
	if len(body) <= len(argv) {
		t.Errorf("the prompt file is %d bytes and the argv %d — the body did not move to the file",
			len(body), len(argv))
	}
}

// A prompt that cannot be written refuses the dispatch outright. An agent
// started without its task is a harness with a shell in the owner's repository
// and no instructions, so there must be no run, no workspace and no agent.
func TestAPromptThatCannotBeWrittenRefusesTheDispatch(t *testing.T) {
	p := newPlaced(t, `"bugtoprompt"`)
	// A regular file where the prompt directory belongs: the MkdirAll
	// inside WritePromptFile cannot get past it.
	blocker := filepath.Join(filepath.Dir(p.dispatcher.Store.Path()), "prompts")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := p.dispatcher.Start(context.Background(), Request{Spec: p.spec(t)})
	if err == nil {
		t.Fatal("a dispatch whose prompt could not be written must be refused")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Errorf("the refusal does not name what failed: %v", err)
	}
	stored, listErr := p.dispatcher.Store.List()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(stored) != 0 {
		t.Errorf("the run store holds %d runs, want none — no dispatch happened", len(stored))
	}
	for _, forbidden := range []string{"workspace create", "agent start", "agent prompt"} {
		if line, ok := hostCall(p.herdrArgv(), forbidden); ok {
			t.Errorf("the host was asked to %q after the refusal", line)
		}
	}
}

// A worktree run keeps the placement it already had: the linked workspace
// Herdr opened for the checkout is that checkout's place in the sidebar, so
// the run is a pane in the feature's tab there and opens no second workspace.
func TestAWorktreeRunStaysInItsLinkedWorkspace(t *testing.T) {
	p := newPlaced(t, `"bugtoprompt"`)
	spec := p.spec(t)
	checkout := t.TempDir()
	run := p.addRun("wt1", "FTR-0001", feature.InProgress, runs.Queued, "")
	run.Worktree = &runs.Worktree{
		Path: checkout, Branch: "ftr-0001-x", Base: "main", WorkspaceID: "wLINKED",
	}
	promptPath, err := WritePromptFile(t.TempDir(), PromptInput{Spec: spec, RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}

	column := p.dispatcher.Config.Column(spec.Status)
	if err := p.dispatcher.launch(context.Background(), Request{Spec: spec}, run,
		roles.Role{Key: "qa"}, column, "claude", promptPath); err != nil {
		t.Fatalf("launch into a worktree: %v", err)
	}

	argv := p.herdrArgv()
	split, ok := hostCall(argv, "pane split")
	if !ok {
		t.Fatalf("a worktree run must still be a pane in its linked workspace; the host saw %v", argv)
	}
	if !strings.Contains(split, "--cwd "+checkout) {
		t.Errorf("the run pane is not in the checkout: %q", split)
	}
	for _, forbidden := range []string{"workspace create", "workspace set-group"} {
		if line, ok := hostCall(argv, forbidden); ok {
			t.Errorf("a worktree run touched workspaces: %q", line)
		}
	}
	updated, err := p.dispatcher.Store.Get("wt1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.WorkspaceID != "wLINKED" || updated.PaneID != "wHOST:p3" {
		t.Errorf("run placed at %s/%s, want the linked workspace and the pane split in it",
			updated.WorkspaceID, updated.PaneID)
	}
	if updated.AnchorPane != "" {
		t.Errorf("AnchorPane = %q, want cleared once the anchor was closed", updated.AnchorPane)
	}
}
