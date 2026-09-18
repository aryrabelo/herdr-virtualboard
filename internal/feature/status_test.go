package feature

import (
	"strings"
	"testing"
)

// The transition table is duplicated from vb-cli so the TUI can grey out
// impossible moves. If vb changes the lifecycle this test is the thing that
// should start failing, so it asserts the whole graph rather than a sample.
func TestAllowedTransitions(t *testing.T) {
	cases := []struct {
		from, to Status
		allowed  bool
	}{
		{Backlog, InProgress, true},
		{Backlog, Review, false},
		{Backlog, Done, false},
		{Backlog, Blocked, false},
		{InProgress, Review, true},
		{InProgress, Blocked, true},
		{InProgress, Done, false},
		{InProgress, Backlog, false},
		{Blocked, InProgress, true},
		{Blocked, Review, false},
		{Review, Done, true},
		{Review, InProgress, true},
		{Review, Blocked, false},
		{Done, InProgress, false},
		{Done, Review, false},
		{Done, Backlog, false},
	}
	for _, tc := range cases {
		if got := VB().CanTransition(tc.from, tc.to); got != tc.allowed {
			t.Errorf("VB().CanTransition(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.allowed)
		}
	}
}

func TestDoneIsTerminal(t *testing.T) {
	if !VB().Terminal(Done) {
		t.Fatal("done must be terminal: VirtualBoard forbids moving a feature out of done")
	}
	for _, status := range []Status{Backlog, InProgress, Blocked, Review} {
		if VB().Terminal(status) {
			t.Errorf("%s must not be terminal", status)
		}
	}
}

func TestParse(t *testing.T) {
	cases := map[string]Status{
		"backlog":     Backlog,
		"IN-PROGRESS": InProgress,
		"in_progress": InProgress,
		"  review  ":  Review,
		"Done":        Done,
	}
	for input, want := range cases {
		got, ok := VB().Parse(input)
		if !ok || got != want {
			t.Errorf("VB().Parse(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	for _, input := range []string{"", "archived", "in progress", "wip"} {
		if got, ok := VB().Parse(input); ok {
			t.Errorf("VB().Parse(%q) = %q, true; want not ok", input, got)
		}
	}
}

// A column the workflow does not declare must not parse, however legal it is
// somewhere else: the move picker turns a parsed name straight into a move.
func TestParseIsScopedToTheWorkflow(t *testing.T) {
	line, err := NewWorkflow([]ColumnDef{{Name: "intake", Next: []Status{"done"}}, {Name: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := line.Parse("intake"); !ok || got != "intake" {
		t.Errorf("Parse(intake) = %q, %v on a workflow that declares it", got, ok)
	}
	if got, ok := line.Parse("backlog"); ok {
		t.Errorf("Parse(backlog) = %q, true on a workflow that does not declare it", got)
	}
	if got, ok := VB().Parse("intake"); ok {
		t.Errorf("VB().Parse(intake) = %q, true; the vb lifecycle has no such state", got)
	}
}

func TestNextIsInBoardOrder(t *testing.T) {
	next := VB().Next(Review)
	if len(next) != 2 || next[0] != InProgress || next[1] != Done {
		t.Fatalf("VB().Next(review) = %v; want [in-progress done] in board order", next)
	}
	if got := VB().Next(Done); len(got) != 0 {
		t.Fatalf("VB().Next(done) = %v; want empty", got)
	}
}

// VB() is one table for the whole process, and Next hands a caller the row a
// move picker renders. A caller that sorted, filtered or overwrote that row in
// place would be editing what vb allows for every later reader — the board
// would grey out a legal move, or offer an illegal one, for the rest of the
// run. The mutation below is what a picker doing its own ordering looks like.
func TestNextCannotRewriteTheSharedTransitionTable(t *testing.T) {
	handed := VB().Next(InProgress)
	if len(handed) != 2 {
		t.Fatalf("VB().Next(in-progress) = %v; want two destinations", handed)
	}
	handed[0] = "done"

	again := VB().Next(InProgress)
	if again[0] != Blocked || again[1] != Review {
		t.Fatalf("VB().Next(in-progress) = %v after a caller wrote to the slice it was handed; want [blocked review]", again)
	}
	if VB().CanTransition(InProgress, Done) {
		t.Fatal("in-progress → done became legal because a caller wrote to the slice Next handed it")
	}
}

func TestDirMatchesVirtualBoardLayout(t *testing.T) {
	if got := InProgress.Dir(); got != "features/in-progress" {
		t.Fatalf("InProgress.Dir() = %q; want features/in-progress", got)
	}
}

// The five vb columns keep the abbreviations the board has always drawn, so a
// golden frame does not change under a generalisation that was meant to add
// columns rather than rename them.
func TestVBKeepsItsHeadings(t *testing.T) {
	vb := VB()
	for _, tc := range []struct {
		status       Status
		title, short string
	}{
		{Backlog, "Backlog", "BACK"},
		{InProgress, "In Progress", "WIP"},
		{Blocked, "Blocked", "BLOCK"},
		{Review, "Review", "REVIEW"},
		{Done, "Done", "DONE"},
	} {
		if got := vb.Title(tc.status); got != tc.title {
			t.Errorf("Title(%s) = %q, want %q", tc.status, got, tc.title)
		}
		if got := vb.Short(tc.status); got != tc.short {
			t.Errorf("Short(%s) = %q, want %q", tc.status, got, tc.short)
		}
	}
}

// A declared column nobody gave a heading to still has to read like a heading,
// and two columns sharing a first word must not share an abbreviation.
func TestHeadingsDeriveFromTheColumnName(t *testing.T) {
	line, err := NewWorkflow([]ColumnDef{
		{Name: "intake"}, {Name: "planning"}, {Name: "first-review"},
		{Name: "ready-to-review"}, {Name: "ready-to-merge"},
		{Name: "pr-verification", Title: "PR Verification", Short: "PRV"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status       Status
		title, short string
	}{
		{"intake", "Intake", "INTAKE"},
		{"planning", "Planning", "PLANNI"},
		{"first-review", "First Review", "FR"},
		{"ready-to-review", "Ready To Review", "RTR"},
		{"ready-to-merge", "Ready To Merge", "RTM"},
		{"pr-verification", "PR Verification", "PRV"},
	} {
		if got := line.Title(tc.status); got != tc.title {
			t.Errorf("Title(%s) = %q, want %q", tc.status, got, tc.title)
		}
		if got := line.Short(tc.status); got != tc.short {
			t.Errorf("Short(%s) = %q, want %q", tc.status, got, tc.short)
		}
	}
}

// A column may name a destination declared to its right, but not one the board
// never draws: that entry could only ever fail, and it would fail at vb rather
// than at the file that declared it.
func TestNewWorkflowRefusesADanglingNext(t *testing.T) {
	_, err := NewWorkflow([]ColumnDef{
		{Name: "intake", Next: []Status{"building"}},
		{Name: "building", Next: []Status{"shipped"}},
	})
	if err == nil {
		t.Fatal("a next naming an undeclared column was accepted")
	}
	for _, want := range []string{"building", "shipped", "next"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if _, err := NewWorkflow([]ColumnDef{
		{Name: "intake", Next: []Status{"building"}}, {Name: "building"},
	}); err != nil {
		t.Fatalf("a forward reference to a declared column was refused: %v", err)
	}
}

func TestNewWorkflowRefusesADuplicateColumn(t *testing.T) {
	_, err := NewWorkflow([]ColumnDef{{Name: "intake"}, {Name: "intake"}})
	if err == nil {
		t.Fatal("the same column declared twice was accepted")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error %q does not say the column was declared twice", err)
	}
}

func TestIndexReportsBoardOrderAndAbsence(t *testing.T) {
	vb := VB()
	if got := vb.Index(Backlog); got != 0 {
		t.Errorf("Index(backlog) = %d, want 0", got)
	}
	if got := vb.Index(Done); got != 4 {
		t.Errorf("Index(done) = %d, want 4", got)
	}
	if got := vb.Index("canceled"); got != -1 {
		t.Errorf("Index(canceled) = %d, want -1 for a column the workflow does not declare", got)
	}
}
