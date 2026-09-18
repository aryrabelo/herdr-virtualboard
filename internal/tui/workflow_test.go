package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// contractLine is the twelve-column production line of ceo-bora#321, as a
// repository declares it.
const contractLine = `
[workflow]
columns = ["intake", "triage", "planning", "building", "first-review",
           "fr-approved", "pr-processing", "pr-verification",
           "ready-to-review", "ready-to-merge", "done", "canceled"]

[columns.first-review]
title = "First Review"
short = "FR"
next = ["building", "fr-approved"]

[columns.ready-to-review]
title = "Ready To Review"
gate = "human"
quiet = true
next = ["ready-to-merge", "building"]
`

// declaredLine loads a `.hvb.toml` and returns the board workflow it declares,
// through config.Load rather than a hand-built Config: the path from the file
// to the columns on screen is the thing worth testing.
func declaredLine(t *testing.T, content string) *feature.Workflow {
	t.Helper()
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.ProjectFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("the declared line must load: %v", err)
	}
	wf, err := cfg.BoardWorkflow()
	if err != nil {
		t.Fatalf("the declared line must build a workflow: %v", err)
	}
	return wf
}

// The twelve declared columns are the board: all of them, in the declared
// order, counted in the header and named in the breadcrumb.
//
// The breadcrumb is what makes a twelve-column line usable at all. Only a
// window of columns fits on a terminal, so the trail is the only thing telling
// the reader that the line continues past the edge of the screen — which is why
// it is asserted abbreviation by abbreviation rather than by length.
func TestADeclaredLineDrawsEveryColumnItNames(t *testing.T) {
	backend := newFakeBackend(
		spec("PR-1", "em verificação", "pr-verification"),
		spec("PR-2", "esperando revisão", "ready-to-review"),
	)
	backend.wf = declaredLine(t, contractLine)
	model := newTestModel(t, backend, 160, 30)

	want := []feature.Status{"intake", "triage", "planning", "building", "first-review",
		"fr-approved", "pr-processing", "pr-verification", "ready-to-review",
		"ready-to-merge", "done", "canceled"}
	order := model.columnOrder()
	if len(order) != len(want) {
		t.Fatalf("the board draws %d columns, want the %d declared: %v", len(order), len(want), order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Errorf("column %d is %q, want %q", index, order[index], want[index])
		}
	}
	if counts := model.Counts(); len(counts) != len(want) {
		t.Errorf("Counts() has %d keys, want one per declared column", len(counts))
	}

	// Frame line 0 is the header; line 1 is the breadcrumb, because twelve
	// columns cannot all be on screen at 160 columns.
	frame := model.Render()
	crumbs := frame[1]
	for _, abbreviation := range []string{"INTAKE", "TRIAGE", "PLANNI", "BUILDI", "FR", "FA",
		"PP", "PV", "RTR", "RTM", "DONE", "CANCEL"} {
		if !strings.Contains(crumbs, abbreviation) {
			t.Errorf("the breadcrumb omits %q: %q", abbreviation, crumbs)
		}
	}
	// And it says the line continues, or a reader takes the window for the
	// whole board.
	if !strings.Contains(crumbs, "›") {
		t.Errorf("the breadcrumb does not mark the columns off screen: %q", crumbs)
	}
	if !strings.Contains(strings.Join(frame, "\n"), "PR VERIFICATION") {
		t.Errorf("the declared title never reaches a column heading\n%s", strings.Join(frame, "\n"))
	}
}

// A window means the focused column is always drawn. Walking right past the
// edge has to scroll the board rather than leave the cursor on a column nobody
// can see.
func TestTheFocusedColumnIsAlwaysInTheWindow(t *testing.T) {
	backend := newFakeBackend()
	backend.wf = declaredLine(t, contractLine)
	model := newTestModel(t, backend, 160, 30)

	order := model.columnOrder()
	for index := range order {
		model.column = index
		start, end := model.columnWindow()
		if index < start || index >= end {
			t.Fatalf("column %d (%s) is outside the drawn window [%d,%d)", index, order[index], start, end)
		}
		if end-start != model.visibleColumns() {
			t.Errorf("the window at column %d is %d wide, want %d", index, end-start, model.visibleColumns())
		}
		body := strings.Join(model.Render()[1:], "\n")
		if !strings.Contains(body, strings.ToUpper(model.workflow.Title(order[index]))) {
			t.Errorf("the focused column %q is not drawn\n%s", order[index], body)
		}
	}
}

// A board opened to answer one question should open on that question. It is the
// mechanism behind a keybinding that lands on Ready To Review.
func TestTheBoardOpensOnTheRequestedColumn(t *testing.T) {
	backend := newFakeBackend(spec("PR-1", "em construção", "building"))
	backend.wf = declaredLine(t, contractLine)

	model := NewModel(backend, Palette{})
	model.Resize(160, 30)
	model.focusColumn = "ready-to-review"
	model.Reload(t.Context())

	if got := model.FocusedStatus(); got != "ready-to-review" {
		t.Fatalf("the board opened on %q, want the requested ready-to-review", got)
	}
}

// A requested column the line does not draw must not strand the cursor on an
// empty board: the request is a preference, and where the work is is the
// fallback the board has always had.
func TestAnUnknownFocusColumnFallsBackToTheWork(t *testing.T) {
	backend := newFakeBackend(spec("PR-1", "em construção", "building"))
	backend.wf = declaredLine(t, contractLine)

	model := NewModel(backend, Palette{})
	model.Resize(160, 30)
	model.focusColumn = "somewhere-else"
	model.Reload(t.Context())

	if got := model.FocusedStatus(); got != "building" {
		t.Fatalf("the board opened on %q, want the only populated column", got)
	}
}

// A declared column vb has never heard of must still be coloured, and two
// neighbours must not share a colour — otherwise a twelve-column board reads
// as five real columns and seven the terminal forgot to paint.
func TestDeclaredColumnsAreColouredApart(t *testing.T) {
	// This shell exports NO_COLOR, which would hand back an empty palette
	// and make every assertion below pass for the wrong reason.
	t.Setenv("NO_COLOR", "")
	palette := NewPalette(true)
	line := declaredLine(t, contractLine)

	seen := map[string]bool{}
	previous := ""
	for _, column := range line.Columns() {
		colour := palette.Status(column, line.Index(column))
		if colour == "" {
			t.Errorf("column %q draws in the terminal's default colour", column)
			continue
		}
		if colour == previous {
			t.Errorf("column %q shares its colour with the column to its left", column)
		}
		previous = colour
		seen[colour] = true
	}
	if len(seen) < 5 {
		t.Errorf("twelve columns use %d colours; the ramp is not being indexed", len(seen))
	}
	// Deterministic: the same column at the same position twice over.
	if first, second := palette.Status("intake", 0), palette.Status("intake", 0); first != second {
		t.Errorf("the same column drew %q then %q", first, second)
	}
	// And a colourless palette stays colourless, or every golden frame in
	// this package grows escape sequences.
	if got := (Palette{}).Status("intake", 0); got != "" {
		t.Errorf("a board with colour off painted a column %q", got)
	}
}
