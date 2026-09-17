package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// `hvb queue` has no VirtualBoard workspace, so it never calls Resolve — and
// Resolve is what used to build the Herdr client. Opening the board inside a
// Herdr pane then dereferenced a nil client and killed the process before it
// drew a single frame.
//
// The context is already cancelled: the callback must come back, not reach the
// network. What is under test is that it comes back at all.
func TestPaneTitleWorksWithoutAResolvedWorkspace(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p1")

	app := &App{}
	if app.Herdr() == nil {
		t.Fatal("the Herdr client is nil, so anything that talks to Herdr panics")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rename := app.paneTitle(ctx)
	if rename == nil {
		t.Fatal("inside a Herdr pane the board must get a rename callback")
	}
	rename("VirtualBoard queue")
}

// Outside a Herdr pane there is nothing to relabel, and the board must not be
// handed a callback that would try.
func TestPaneTitleIsNilOutsideAPane(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")

	if rename := (&App{}).paneTitle(context.Background()); rename != nil {
		t.Error("the board got a rename callback with no pane to rename")
	}
}

// charterDir writes the charters a queue board would load. The two files
// roles.Load skips are included: a directory holding only those is not a
// directory holding roles, and the caller has to be told so.
func charterDir(t *testing.T, parent string) string {
	t.Helper()
	dir := filepath.Join(parent, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"backend_dev.md": "# Backend Dev\n\nBuild the server side.\n",
		"qa.md":          "# QA\n\nReview against the acceptance criteria.\n",
		"AGENTS.md":      "# Agents\n\nNot a role.\n",
		"RULES.md":       "# Rules\n\nNot a role.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Dispatching needs both halves: charters to pick a role from and a repository
// to run in. Accepting one alone would produce a board where d opens a picker
// that cannot launch anything, or launches into nowhere — so the refusal has to
// name the half that is missing, not merely fail.
func TestQueueDispatchRefusesHalfOfThePair(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	charters := charterDir(t, dir)

	cases := []struct {
		name     string
		charters string
		workRoot string
		want     string
	}{
		{"charters alone", charters, "", "--work-root"},
		{"work root alone", "", dir, "--charters"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dispatcher, err := queueDispatch(&App{}, &cfg, testCase.charters, testCase.workRoot, "")
			if dispatcher != nil {
				t.Fatal("half a pair built a dispatcher")
			}
			var usage UsageError
			if !errors.As(err, &usage) {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("the refusal does not name %s: %v", testCase.want, err)
			}
		})
	}
}

// Neither flag is the board `hvb queue` has always been, and it must stay
// buildable without a repository, a charter directory or a run store.
func TestQueueDispatchIsAbsentWhenNeitherFlagIsGiven(t *testing.T) {
	cfg := config.Default()
	dispatcher, err := queueDispatch(&App{}, &cfg, "", "", t.TempDir())
	if err != nil {
		t.Fatalf("a read-only board failed to open: %v", err)
	}
	if dispatcher != nil {
		t.Error("a board asked for no dispatch got one anyway")
	}
}

// A vault is a VirtualBoard workspace too, so the charters are usually already
// next to the FIOS.md the board is reading. Deriving them saves a flag the user
// would otherwise have to spell out identically every time.
func TestQueueDispatchDerivesChartersFromTheVault(t *testing.T) {
	t.Setenv("HVB_DATA_DIR", t.TempDir())
	cfg := config.Default()
	vaultRoot := t.TempDir()
	charterDir(t, filepath.Join(vaultRoot, workspace.Dir))

	dispatcher, err := queueDispatch(&App{}, &cfg, "", t.TempDir(), vaultRoot)
	if err != nil {
		t.Fatalf("the vault's own charters were not used: %v", err)
	}
	if got := roleKeys(dispatcher.Roles); got != "backend_dev,qa" {
		t.Errorf("roles = %q, want the vault's two charters", got)
	}
}

// A vault without charters cannot answer the question, and the user asked to
// dispatch. Falling through to a role-less board would report it as read-only,
// blaming flags that were in fact passed.
func TestQueueDispatchSaysWhenTheVaultHasNoCharters(t *testing.T) {
	cfg := config.Default()
	_, err := queueDispatch(&App{}, &cfg, "", t.TempDir(), t.TempDir())
	var usage UsageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--charters") {
		t.Fatalf("err = %v, want a usage error naming --charters", err)
	}
}

// A directory holding only AGENTS.md and RULES.md holds no roles: roles.Load
// answers zero and no error, which on this board means "cannot dispatch". The
// board would then blame the flags the user just passed.
func TestQueueDispatchRejectsACharterDirectoryWithNoRoles(t *testing.T) {
	cfg := config.Default()
	empty := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "RULES.md"} {
		if err := os.WriteFile(filepath.Join(empty, name), []byte("# not a role\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := queueDispatch(&App{}, &cfg, empty, t.TempDir(), "")
	var usage UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

// A work root that is not there is a dispatch that would fail at the first
// pane, in a directory the user believes exists.
func TestQueueDispatchRejectsAMissingWorkRoot(t *testing.T) {
	cfg := config.Default()
	charters := charterDir(t, t.TempDir())

	_, err := queueDispatch(&App{}, &cfg, charters, filepath.Join(t.TempDir(), "nope"), "")
	var usage UsageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--work-root") {
		t.Fatalf("err = %v, want a usage error naming --work-root", err)
	}
}

// The run store is one file per project id. A queue board keyed like a
// VirtualBoard workspace would write its PR- and issue-keyed runs into the
// history of a real project whenever --work-root is a vb workspace, which is
// the likely case: that is where the agent works.
func TestQueueDispatchKeepsItsRunsOutOfAVirtualBoardProject(t *testing.T) {
	t.Setenv("HVB_DATA_DIR", t.TempDir())
	cfg := config.Default()
	root := t.TempDir()
	charters := charterDir(t, t.TempDir())

	dispatcher, err := queueDispatch(&App{}, &cfg, charters, root, "")
	if err != nil {
		t.Fatal(err)
	}
	vbID := (&workspace.Workspace{Root: dispatcher.Workspace.Root}).ID()
	store := filepath.Base(dispatcher.Store.Path())
	if store == vbID+".json" {
		t.Fatalf("the queue board writes into %s, the run file of the vb project at the same root", store)
	}
	if !strings.HasPrefix(store, "queue-") {
		t.Errorf("run store = %q, want a queue- keyed file", store)
	}
}

// roleKeys is the loaded charters as a comparable string.
func roleKeys(loaded []roles.Role) string {
	keys := make([]string, 0, len(loaded))
	for _, role := range loaded {
		keys = append(keys, role.Key)
	}
	return strings.Join(keys, ",")
}
