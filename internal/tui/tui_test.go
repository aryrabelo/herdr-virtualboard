package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/netors/herdr-virtualboard/internal/config"
	"github.com/netors/herdr-virtualboard/internal/feature"
	"github.com/netors/herdr-virtualboard/internal/roles"
	"github.com/netors/herdr-virtualboard/internal/runs"
)

// fakeBackend drives the model without a workspace, a vb binary, or a Herdr.
// It records the calls the board makes so a test can assert that pressing a key
// asked for the right change, not merely that the screen redrew.
type fakeBackend struct {
	specs []*feature.Spec
	runs  []*runs.Run
	cfg   config.Config

	moves     []string
	created   []string
	dispatch  []string
	cancelled []string
	focused   []string
	fields    []string

	moveErr     error
	dispatchErr error
}

func newFakeBackend(specs ...*feature.Spec) *fakeBackend {
	return &fakeBackend{specs: specs, cfg: config.Default()}
}

func (f *fakeBackend) Load(context.Context) ([]*feature.Spec, []*runs.Run, []error) {
	return f.specs, f.runs, nil
}

func (f *fakeBackend) Move(_ context.Context, id string, target feature.Status, owner string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moves = append(f.moves, fmt.Sprintf("%s→%s@%s", id, target, owner))
	for _, spec := range f.specs {
		if spec.ID == id {
			spec.Status = target
		}
	}
	return nil
}

func (f *fakeBackend) Create(_ context.Context, title string, labels []string, priority string) (string, error) {
	f.created = append(f.created, fmt.Sprintf("%s|%s|%s", title, strings.Join(labels, ","), priority))
	return "FTR-9001", nil
}

func (f *fakeBackend) SetField(_ context.Context, id, key, value string) error {
	f.fields = append(f.fields, fmt.Sprintf("%s.%s=%s", id, key, value))
	return nil
}

func (f *fakeBackend) Dispatch(_ context.Context, spec *feature.Spec, role, kind string, _ bool) (*runs.Run, error) {
	if f.dispatchErr != nil {
		return nil, f.dispatchErr
	}
	f.dispatch = append(f.dispatch, fmt.Sprintf("%s/%s", spec.ID, role))
	return &runs.Run{ID: "r-new", FeatureID: spec.ID, Role: role, Kind: "claude",
		State: runs.Running, PaneID: "w1:p9", StartedAt: time.Now()}, nil
}

func (f *fakeBackend) Cancel(_ context.Context, runID string) error {
	f.cancelled = append(f.cancelled, runID)
	return nil
}

func (f *fakeBackend) Focus(_ context.Context, runID string) error {
	f.focused = append(f.focused, runID)
	return nil
}

func (f *fakeBackend) Roles() []roles.Role {
	return []roles.Role{
		{Key: "backend_dev", Description: "Backend APIs"},
		{Key: "frontend_dev", Description: "UI"},
		{Key: "fullstack_dev", Description: "End to end"},
		{Key: "qa", Description: "Testing"},
	}
}

func (f *fakeBackend) Config() *config.Config { return &f.cfg }
func (f *fakeBackend) Owner() string          { return "tester" }
func (f *fakeBackend) ProjectName() string    { return "demo" }

func spec(id, title string, status feature.Status, labels ...string) *feature.Spec {
	return &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: id, Title: title, Status: status, Priority: "P2",
		Created: "2026-01-01", Updated: "2026-01-02", Labels: labels,
	}}
}

func newTestModel(t *testing.T, backend Backend, width, height int) *Model {
	t.Helper()
	// No palette: assertions read the text, not the escape sequences.
	model := NewModel(backend, Palette{})
	model.Resize(width, height)
	model.Reload(context.Background())
	return model
}

func screen(model *Model) string { return strings.Join(model.Render(), "\n") }

func TestRenderShowsEveryLifecycleColumn(t *testing.T) {
	backend := newFakeBackend(
		spec("FTR-0001", "First thing", feature.Backlog),
		spec("FTR-0002", "Second thing", feature.InProgress),
		spec("FTR-0003", "Third thing", feature.Done),
	)
	model := newTestModel(t, backend, 140, 30)
	out := screen(model)
	for _, want := range []string{"BACKLOG", "IN PROGRESS", "BLOCKED", "REVIEW", "DONE",
		"0001", "First thing", "0002", "0003"} {
		if !strings.Contains(out, want) {
			t.Errorf("board is missing %q\n%s", want, out)
		}
	}
}

// Every rendered frame must be exactly the terminal height, and no line may
// overflow the width — an off-by-one here corrupts the surrounding pane.
func TestRenderRespectsGeometry(t *testing.T) {
	backend := newFakeBackend()
	for index := 0; index < 30; index++ {
		backend.specs = append(backend.specs,
			spec(fmt.Sprintf("FTR-%04d", index), strings.Repeat("long title ", 8), feature.Backlog))
	}
	for _, size := range [][2]int{{140, 40}, {110, 24}, {80, 20}, {48, 16}, {36, 12}} {
		model := newTestModel(t, backend, size[0], size[1])
		lines := model.Render()
		if len(lines) != size[1] {
			t.Errorf("%dx%d: got %d lines, want %d", size[0], size[1], len(lines), size[1])
		}
		for index, line := range lines {
			if w := displayWidth(line); w > size[0] {
				t.Errorf("%dx%d: line %d is %d columns wide", size[0], size[1], index, w)
			}
		}
	}
}

// A phone-width terminal shows one column with a breadcrumb rather than five
// unreadable slivers.
// The vertical rule between columns must run the full height. Stopping it below
// the last card reads as a broken frame rather than an empty column — and the
// blank rows are exactly where it is easiest to forget.
func TestColumnSeparatorsRunTheFullHeight(t *testing.T) {
	backend := newFakeBackend(
		spec("FTR-0001", "A", feature.Backlog),
		spec("FTR-0002", "B", feature.Backlog),
		spec("FTR-0003", "C", feature.Backlog),
	)
	model := newTestModel(t, backend, 140, 30)
	lines := model.Render()

	// Skip the header and footer; the body is everything between.
	body := lines[1 : len(lines)-1]
	want := strings.Count(body[0], "│")
	if want < 4 {
		t.Fatalf("the first body row has %d separators, want one per column boundary: %q", want, body[0])
	}
	for index, line := range body {
		if got := strings.Count(line, "│"); got != want {
			t.Errorf("body row %d has %d separators, want %d: %q", index, got, want, line)
		}
	}
}

// Every rendered row must be the full terminal width, blank ones included, or
// the pane behind the board shows through.
func TestEveryRowIsFullWidth(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.Backlog))
	for _, width := range []int{140, 120, 108} {
		model := newTestModel(t, backend, width, 24)
		for index, line := range model.Render()[1:23] {
			if got := displayWidth(line); got != width {
				t.Errorf("%d columns: body row %d is %d wide", width, index, got)
			}
		}
	}
}

func TestNarrowTerminalUsesTheSingleColumnLayout(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "Only thing", feature.Backlog))
	wide := newTestModel(t, backend, 140, 30)
	if wide.compact() {
		t.Error("140 columns should not be compact")
	}
	narrow := newTestModel(t, backend, 44, 20)
	if !narrow.compact() {
		t.Fatal("44 columns should be compact")
	}
	out := screen(narrow)
	if !strings.Contains(out, "Only thing") {
		t.Errorf("the focused column's card is missing:\n%s", out)
	}
	// The breadcrumb names every stage so the user still knows where they are.
	for _, crumb := range []string{"BACK", "WIP", "REVIEW", "DONE"} {
		if !strings.Contains(out, crumb) {
			t.Errorf("breadcrumb is missing %q\n%s", crumb, out)
		}
	}
}

// The board opens where the work is, not on an empty leftmost column.
func TestBoardOpensOnTheFirstPopulatedColumn(t *testing.T) {
	backend := newFakeBackend(
		spec("FTR-0002", "B", feature.Review),
		spec("FTR-0003", "C", feature.Done),
	)
	model := newTestModel(t, backend, 140, 30)
	if got := model.FocusedStatus(); got != feature.Review {
		t.Fatalf("opened on %s, want review", got)
	}
}

func TestNavigationMovesBetweenColumnsAndCards(t *testing.T) {
	backend := newFakeBackend(
		spec("FTR-0001", "A", feature.Backlog),
		spec("FTR-0002", "B", feature.Backlog),
		spec("FTR-0003", "C", feature.InProgress),
	)
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()

	if got := model.FocusedCard().Spec.ID; got != "FTR-0001" {
		t.Fatalf("initial focus = %s", got)
	}
	model.Handle(ctx, Key{Name: KeyDown})
	if got := model.FocusedCard().Spec.ID; got != "FTR-0002" {
		t.Fatalf("after down = %s", got)
	}
	// Moving past the end must clamp, not wrap: wrapping makes a long column
	// feel like it lost your place.
	model.Handle(ctx, Key{Name: KeyDown})
	if got := model.FocusedCard().Spec.ID; got != "FTR-0002" {
		t.Fatalf("after down past the end = %s, want it clamped", got)
	}
	model.Handle(ctx, Key{Rune: 'l'})
	if got := model.FocusedStatus(); got != feature.InProgress {
		t.Fatalf("after right = %s", got)
	}
	if got := model.FocusedCard().Spec.ID; got != "FTR-0003" {
		t.Fatalf("focused card in the next column = %s", got)
	}
}

// A refresh must not silently move the selection: that is how a user dispatches
// the wrong feature.
func TestReloadKeepsTheFocusedFeature(t *testing.T) {
	backend := newFakeBackend(
		spec("FTR-0001", "A", feature.Backlog),
		spec("FTR-0002", "B", feature.Backlog),
	)
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Name: KeyDown})
	focused := model.FocusedCard().Spec.ID

	// Another agent moves the focused feature to a different column.
	backend.specs[1].Status = feature.InProgress
	model.Reload(context.Background())

	if got := model.FocusedCard().Spec.ID; got != focused {
		t.Fatalf("focus drifted from %s to %s across a reload", focused, got)
	}
	if got := model.FocusedStatus(); got != feature.InProgress {
		t.Fatalf("focus should have followed the feature to %s, got %s", feature.InProgress, got)
	}
}

// The lifecycle should be visible, not merely enforced: an illegal destination
// is shown and greyed out, because absence looks like a bug.
func TestMovePickerShowsIllegalDestinationsAsDisabled(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.Backlog))
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'm'})

	if model.view != ViewMovePicker || model.picker == nil {
		t.Fatal("m should open the move picker")
	}
	enabled := map[string]bool{}
	for _, entry := range model.picker.options {
		enabled[entry.Value] = !entry.Disabled
	}
	if !enabled["in-progress"] {
		t.Error("backlog → in-progress must be offered")
	}
	for _, illegal := range []string{"review", "done", "blocked"} {
		if enabled[illegal] {
			t.Errorf("backlog → %s is illegal and must be disabled", illegal)
		}
	}
	// The selection must land on something choosable.
	if chosen, ok := model.picker.current(); !ok || chosen.Disabled {
		t.Fatalf("the picker opened on a disabled row: %+v", chosen)
	}
}

func TestMovePickerPerformsTheMove(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.Backlog))
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'm'})
	model.Handle(ctx, Key{Name: KeyEnter})

	if len(backend.moves) != 1 || !strings.HasPrefix(backend.moves[0], "FTR-0001→in-progress") {
		t.Fatalf("moves = %v", backend.moves)
	}
	// Claiming a feature for work records who is working it.
	if !strings.HasSuffix(backend.moves[0], "@tester") {
		t.Errorf("moving into in-progress should set the owner: %q", backend.moves[0])
	}
}

// H and L are the quick path; an illegal shift must explain the lifecycle
// rather than silently doing nothing.
func TestShiftRefusesAnIllegalMove(t *testing.T) {
	// blocked sits between in-progress and review on the board, but the only
	// move out of it is back to in-progress.
	backend := newFakeBackend(spec("FTR-0001", "A", feature.Blocked))
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'L'})

	if len(backend.moves) != 0 {
		t.Fatalf("a shift off the left edge must not move anything: %v", backend.moves)
	}
	message, isError := model.statusMessage()
	if !isError || !strings.Contains(message, "does not allow") {
		t.Fatalf("status = %q (error=%v), want a lifecycle explanation", message, isError)
	}
}

func TestShiftPerformsALegalMove(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.Backlog))
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'L'})

	if len(backend.moves) != 1 {
		t.Fatalf("moves = %v", backend.moves)
	}
}

// The dispatch picker should open on the role the feature's labels suggest, so
// the common case is one keystroke.
func TestDispatchPickerPreselectsTheSuggestedRole(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "API work", feature.InProgress, "backend"))
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'd'})

	if model.view != ViewDispatchPicker || model.picker == nil {
		t.Fatal("d should open the dispatch picker")
	}
	chosen, ok := model.picker.current()
	if !ok || chosen.Value != "backend_dev" {
		t.Fatalf("preselected %q, want backend_dev", chosen.Value)
	}
}

func TestDispatchPickerStartsTheRun(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "API work", feature.InProgress, "backend"))
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'd'})
	model.Handle(ctx, Key{Name: KeyEnter})

	if len(backend.dispatch) != 1 || backend.dispatch[0] != "FTR-0001/backend_dev" {
		t.Fatalf("dispatch = %v", backend.dispatch)
	}
}

// Two agents on one feature is the failure mode worth preventing outright.
func TestDispatchRefusesAFeatureThatAlreadyHasARun(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.InProgress))
	backend.runs = []*runs.Run{{
		ID: "r1", FeatureID: "FTR-0001", State: runs.Running,
		Role: "backend_dev", StartedAt: time.Now(),
	}}
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'd'})

	if model.view == ViewDispatchPicker {
		t.Fatal("dispatch should be refused while a run is in progress")
	}
	message, isError := model.statusMessage()
	if !isError || !strings.Contains(message, "already has a run") {
		t.Fatalf("status = %q", message)
	}
}

func TestNewFeatureFormCreates(t *testing.T) {
	backend := newFakeBackend()
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'n'})
	if model.view != ViewNewFeature {
		t.Fatal("n should open the new-feature form")
	}
	for _, r := range "Retry uploads" {
		model.Handle(ctx, Key{Rune: r})
	}
	model.Handle(ctx, Key{Name: KeyTab})
	for _, r := range "backend reliability" {
		model.Handle(ctx, Key{Rune: r})
	}
	model.Handle(ctx, Key{Name: KeyEnter})

	if len(backend.created) != 1 {
		t.Fatalf("created = %v", backend.created)
	}
	if backend.created[0] != "Retry uploads|backend,reliability|P2" {
		t.Fatalf("created = %q", backend.created[0])
	}
}

func TestNewFeatureFormRequiresATitle(t *testing.T) {
	backend := newFakeBackend()
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'n'})
	model.Handle(ctx, Key{Name: KeyEnter})

	if len(backend.created) != 0 {
		t.Fatalf("an empty title must not create anything: %v", backend.created)
	}
	if model.view != ViewNewFeature {
		t.Fatal("the form should stay open so the user can fix it")
	}
	if model.form.err == "" {
		t.Fatal("the form should say what is wrong")
	}
}

// Cancelling a run destroys work, so it asks first.
func TestCancelAsksForConfirmation(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.InProgress))
	backend.runs = []*runs.Run{{
		ID: "r1", FeatureID: "FTR-0001", State: runs.Running,
		Role: "backend_dev", PaneID: "w1:p1", StartedAt: time.Now(),
	}}
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'x'})

	if model.view != ViewConfirm {
		t.Fatal("x should ask before cancelling")
	}
	if len(backend.cancelled) != 0 {
		t.Fatal("nothing should be cancelled before confirmation")
	}
	model.Handle(ctx, Key{Name: KeyEsc})
	if len(backend.cancelled) != 0 {
		t.Fatal("escape must abandon the cancellation")
	}

	model.Handle(ctx, Key{Rune: 'x'})
	model.Handle(ctx, Key{Rune: 'y'})
	if len(backend.cancelled) != 1 || backend.cancelled[0] != "r1" {
		t.Fatalf("cancelled = %v", backend.cancelled)
	}
}

func TestDetailViewShowsTheSpec(t *testing.T) {
	target := spec("FTR-0007", "Add retry", feature.InProgress, "backend")
	target.Body = "## Summary\nRetry failed PUTs.\n\n## Acceptance Criteria\n- [x] retries\n- [ ] backoff\n"
	backend := newFakeBackend(target)
	backend.runs = []*runs.Run{{
		ID: "r1", FeatureID: "FTR-0007", State: runs.Succeeded, Role: "backend_dev",
		Kind: "claude", Outcome: "success", MovedTo: "review", StartedAt: time.Now().Add(-time.Hour),
	}}
	model := newTestModel(t, backend, 120, 40)
	model.Handle(context.Background(), Key{Name: KeyEnter})

	if model.view != ViewDetail {
		t.Fatal("enter should open the detail view")
	}
	out := screen(model)
	for _, want := range []string{"FTR-0007", "Add retry", "Acceptance criteria", "(1/2)",
		"Retry failed PUTs.", "Runs", "succeeded", "moved to review"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail is missing %q\n%s", want, out)
		}
	}
}

// The delimiters are meaningful to an agent but noise to a reader.
func TestDetailStripsTheUntrustedMarkers(t *testing.T) {
	target := spec("FTR-0007", "Add retry", feature.Backlog)
	target.Body = "<untrusted-content>\n<!-- a note -->\n\n## Summary\nThe real text.\n\n</untrusted-content>\n"
	model := newTestModel(t, newFakeBackend(target), 120, 30)
	model.Handle(context.Background(), Key{Name: KeyEnter})

	out := screen(model)
	if strings.Contains(out, "untrusted-content") {
		t.Errorf("the delimiters leaked into the reader's view:\n%s", out)
	}
	if !strings.Contains(out, "The real text.") {
		t.Errorf("the summary is missing:\n%s", out)
	}
}

func TestEscapeFromDetailReturnsToTheBoard(t *testing.T) {
	model := newTestModel(t, newFakeBackend(spec("FTR-0001", "A", feature.Backlog)), 120, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Name: KeyEnter})
	model.Handle(ctx, Key{Name: KeyEsc})
	if model.view != ViewBoard {
		t.Fatalf("view = %v, want the board", model.view)
	}
	if model.Quitting() {
		t.Fatal("escaping a detail view must not quit the board")
	}
}

func TestHelpListsTheLifecycle(t *testing.T) {
	model := newTestModel(t, newFakeBackend(), 120, 40)
	model.Handle(context.Background(), Key{Rune: '?'})
	out := screen(model)
	for _, want := range []string{"backlog", "in-progress", "terminal", "dispatch"} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q\n%s", want, out)
		}
	}
}

func TestQuitOnlyFromTheBoard(t *testing.T) {
	model := newTestModel(t, newFakeBackend(spec("FTR-0001", "A", feature.Backlog)), 120, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'q'})
	if !model.Quitting() {
		t.Fatal("q on the board should quit")
	}
}

func TestCtrlCAlwaysQuits(t *testing.T) {
	model := newTestModel(t, newFakeBackend(spec("FTR-0001", "A", feature.Backlog)), 120, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'n'})
	model.Handle(ctx, Key{Name: KeyCtrlC})
	if !model.Quitting() {
		t.Fatal("ctrl-c must quit even from inside a form")
	}
}

func TestDispatchErrorIsShownWithoutLosingTheBoard(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.InProgress, "backend"))
	backend.dispatchErr = fmt.Errorf("herdr is not running")
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'd'})
	model.Handle(ctx, Key{Name: KeyEnter})

	message, isError := model.statusMessage()
	if !isError || !strings.Contains(message, "herdr is not running") {
		t.Fatalf("status = %q (error=%v)", message, isError)
	}
	if model.view != ViewBoard {
		t.Fatalf("view = %v, want to be back on the board", model.view)
	}
}

func TestPriorityPickerSetsTheField(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "A", feature.Backlog))
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()
	model.Handle(ctx, Key{Rune: 'e'})
	model.Handle(ctx, Key{Name: KeyUp}) // P2 → P1
	model.Handle(ctx, Key{Name: KeyEnter})

	if len(backend.fields) != 1 || backend.fields[0] != "FTR-0001.priority=P1" {
		t.Fatalf("fields = %v", backend.fields)
	}
}

func TestEmptyBoardRenders(t *testing.T) {
	model := newTestModel(t, newFakeBackend(), 140, 30)
	out := screen(model)
	if !strings.Contains(out, "BACKLOG") {
		t.Errorf("an empty board should still show its columns:\n%s", out)
	}
	if model.FocusedCard() != nil {
		t.Error("an empty board has no focused card")
	}
	// Keys that need a card must be harmless rather than panicking.
	ctx := context.Background()
	for _, key := range []Key{{Rune: 'd'}, {Rune: 'm'}, {Rune: 'x'}, {Rune: 'o'}, {Name: KeyEnter}} {
		model.Handle(ctx, key)
	}
}

func TestTinyTerminalDegradesGracefully(t *testing.T) {
	model := newTestModel(t, newFakeBackend(spec("FTR-0001", "A", feature.Backlog)), 20, 5)
	lines := model.Render()
	if len(lines) == 0 {
		t.Fatal("even a tiny terminal should render something")
	}
	if !strings.Contains(strings.Join(lines, " "), "too small") {
		t.Errorf("a terminal below the minimum should say so: %v", lines)
	}
}
