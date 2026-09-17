package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/dispatch"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// unknownHarness is a kind Herdr cannot start, and it is how these tests reach
// dispatch.Start and stop inside it. Start validates the harness before it
// gates on Herdr (dispatch.go:105-113), so the decision under test — does this
// board delegate, or refuse? — is observable without a pane, a socket, a vb
// lock or a git repository ever being touched.
const unknownHarness = "no-such-harness"

// dispatchCharters writes two real charters plus the two files roles.Load
// skips, so the loaded set is exactly what a charter directory yields.
func dispatchCharters(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agents")
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

// dispatchingBackend is a queue board with the dispatch half wired, as
// `hvb queue --charters --work-root` builds it.
//
// Herdr and VB are left nil deliberately. With the lock off nothing on the
// paths under test calls either of them, and a nil field is how that stays
// true: a test that starts reaching Herdr panics instead of quietly shelling
// out to the real binary.
func dispatchingBackend(t *testing.T, cfg *config.Config, sources ...Source) *SourceBackend {
	t.Helper()
	t.Setenv("HVB_DATA_DIR", t.TempDir())
	store, err := runs.Open("queue-test")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := roles.Load(dispatchCharters(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return NewSourceBackendWithDispatch("vault + repo", cfg, "ary", &dispatch.Dispatcher{
		Workspace: &workspace.Workspace{Root: root, Board: filepath.Join(root, workspace.Dir)},
		Config:    cfg,
		Store:     store,
		Roles:     loaded,
	}, sources...)
}

// Without the dispatch pair the board is what it always was, and that is not a
// detail: a board with no repository to work in must refuse rather than launch
// an agent into nowhere, and the refusal has to be an ErrReadOnly so the TUI
// reports it as a refusal instead of as a broken dispatch.
func TestABoardWithoutTheDispatchPairStaysReadOnly(t *testing.T) {
	card := prCard("179", "board: ler a fila real", feature.Review)
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{specs: []*feature.Spec{card}})
	backend.Load(context.Background())
	ctx := context.Background()

	if got := backend.Roles(); len(got) != 0 {
		t.Errorf("Roles() = %v, want none on a board that cannot dispatch", got)
	}
	if _, err := backend.Dispatch(ctx, card, "qa", "", false); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Dispatch err = %v, want an ErrReadOnly", err)
	}
	if _, err := backend.DispatchWith(ctx, card, "qa", "", nil, nil); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DispatchWith err = %v, want an ErrReadOnly", err)
	}
	if err := backend.DispatchUnavailable(card); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DispatchUnavailable err = %v, want an ErrReadOnly", err)
	}
	// The picker only asks DispatchUnavailable, so its text is the one the
	// user reads. It has to name the flags that turn dispatch on: the old
	// message blamed absent charters in .virtualboard/agents, a directory
	// this board never reads.
	err := backend.DispatchUnavailable(card)
	for _, want := range []string{"--charters", "--work-root", "PR-179"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if err := backend.Cancel(ctx, "run-1"); err == nil {
		t.Error("a board with no runs accepted a cancel")
	}
	if err := backend.Focus(ctx, "run-1"); err == nil {
		t.Error("a board with no runs accepted a focus")
	}
}

// With the pair wired, d has to reach the dispatcher: the charters become the
// picker's options, and a dispatch is a real dispatch. A board that kept
// refusing here is the bug this change exists to fix.
func TestADispatchingBoardDelegatesInsteadOfRefusing(t *testing.T) {
	cfg := config.Default()
	card := prCard("179", "board: ler a fila real", feature.Review)
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})
	ctx := context.Background()

	keys := make([]string, 0, 2)
	for _, role := range backend.Roles() {
		keys = append(keys, role.Key)
	}
	if strings.Join(keys, ",") != "backend_dev,qa" {
		t.Errorf("Roles() = %v, want the charters the board was given", keys)
	}

	_, err := backend.Dispatch(ctx, card, "qa", unknownHarness, false)
	if errors.Is(err, ErrReadOnly) {
		t.Fatalf("Dispatch still refused the card: %v", err)
	}
	if !errors.Is(err, dispatch.ErrUnknownHarness) {
		t.Fatalf("Dispatch err = %v, want the dispatcher's own harness check", err)
	}
	if _, err := backend.DispatchWith(ctx, card, "qa", unknownHarness, nil, nil); !errors.Is(err, dispatch.ErrUnknownHarness) {
		t.Fatalf("DispatchWith err = %v, want the dispatcher's own harness check", err)
	}
}

// Dispatching is the only write this board gains. The card still lives in
// FIOS.md, in a gate ledger or on GitHub, and the board still has no parser to
// write any of them — so every card mutation stays a refusal, dispatcher or no
// dispatcher.
func TestTheCardsStayReadOnlyEvenWithADispatcher(t *testing.T) {
	cfg := config.Default()
	card := prCard("179", "board: ler a fila real", feature.Review)
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})
	backend.Load(context.Background())
	ctx := context.Background()

	if err := backend.Move(ctx, card.ID, feature.Done, "ary"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Move err = %v, want an ErrReadOnly", err)
	}
	if _, err := backend.Create(ctx, "algo novo", nil, "P1"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Create err = %v, want an ErrReadOnly", err)
	}
	if err := backend.SetField(ctx, card.ID, "priority", "P0"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("SetField err = %v, want an ErrReadOnly", err)
	}
}

// Dispatch must run with the vb lock off — vb has never heard of PR-179 — but
// the board reads the same *config.Config for its columns and its harness, and
// `hvb tui` hands that pointer to a backend that does lock. Zeroing the field
// in place would turn locking off for everything sharing it, silently.
func TestTheDispatchLockIsOffWithoutTouchingTheBoardsConfig(t *testing.T) {
	cfg := config.Default()
	if cfg.LockTTLMinutes <= 0 {
		t.Fatalf("LockTTLMinutes = %d: the default no longer locks, so this proves nothing", cfg.LockTTLMinutes)
	}
	before := cfg.LockTTLMinutes
	card := prCard("179", "board: ler a fila real", feature.Review)
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})

	if _, err := backend.Dispatch(context.Background(), card, "qa", unknownHarness, false); err == nil {
		t.Fatal("the unknown harness was accepted, so no dispatch was attempted")
	}

	if cfg.LockTTLMinutes != before {
		t.Errorf("the shared config lost its lock: LockTTLMinutes = %d, want %d", cfg.LockTTLMinutes, before)
	}
	if got := backend.Config().LockTTLMinutes; got != before {
		t.Errorf("the board reads LockTTLMinutes = %d, want %d", got, before)
	}
	if got := backend.dispatcher.Config.LockTTLMinutes; got != 0 {
		t.Errorf("the dispatcher would claim a vb lock: LockTTLMinutes = %d, want 0", got)
	}
	if backend.dispatcher.Config == backend.Config() {
		t.Error("the dispatcher and the board share one config, so one of them is wrong")
	}
}

// A run this board dispatched is a pane this board created, so cancelling or
// focusing it is not a write to anybody's file. Both have to reach the
// dispatcher — the run store is the only thing that knows the id.
func TestCancelAndFocusFollowTheBoardsOwnRuns(t *testing.T) {
	cfg := config.Default()
	backend := dispatchingBackend(t, &cfg, &fakeSource{})
	ctx := context.Background()

	if err := backend.Cancel(ctx, "run-nope"); !errors.Is(err, runs.ErrNotFound) {
		t.Errorf("Cancel err = %v, want the run store's verdict", err)
	}
	if err := backend.Focus(ctx, "run-nope"); !errors.Is(err, runs.ErrNotFound) {
		t.Errorf("Focus err = %v, want the run store's verdict", err)
	}
}

// A dispatched run has to come back on the next refresh, or the card shows no
// agent and the user dispatches a second one onto the same work.
func TestLoadReturnsTheRunsThisBoardDispatched(t *testing.T) {
	cfg := config.Default()
	card := prCard("179", "board: ler a fila real", feature.Review)
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})

	// A settled run: Reconcile only reads Herdr for runs still holding a
	// pane (complete.go:228-241), and this board's Herdr client is nil.
	stored := &runs.Run{
		ID: "run-1", FeatureID: card.ID, Title: card.Title, Status: card.Status,
		Role: "qa", Kind: "claude", State: runs.Succeeded, StartedAt: time.Now().UTC(),
	}
	if err := backend.dispatcher.Store.Append(stored); err != nil {
		t.Fatal(err)
	}

	_, loaded, problems := backend.Load(context.Background())
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if len(loaded) != 1 || loaded[0].ID != "run-1" {
		t.Fatalf("runs = %v, want the run this board dispatched", loaded)
	}
}

// A board with no dispatcher has no store to read, and reporting a problem for
// that would put a permanent error on a board that is working as intended.
func TestAReadOnlyBoardReportsNoRunsAndNoProblem(t *testing.T) {
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{
		specs: []*feature.Spec{prCard("179", "board", feature.Review)},
	})

	_, loaded, problems := backend.Load(context.Background())
	if len(loaded) != 0 {
		t.Errorf("runs = %v, want none", loaded)
	}
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none", problems)
	}
}
