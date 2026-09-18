package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// contractLine is the twelve-column production line of ceo-bora#321, declared
// the way a repository declares it.
const contractLine = `
[workflow]
columns = ["intake", "triage", "planning", "building", "first-review",
           "fr-approved", "pr-processing", "pr-verification",
           "ready-to-review", "ready-to-merge", "done", "canceled"]

[columns.building]
auto = true
role = "backend_dev"
next = ["first-review"]

[columns.first-review]
title = "First Review"
short = "FR"
next = ["building", "fr-approved"]

[columns.pr-verification]
title = "PR Verification"
when = "hvb:state:open"
next = ["building", "ready-to-review"]

[columns.ready-to-review]
title = "Ready To Review"
gate = "human"
quiet = true
next = ["ready-to-merge", "building"]

[columns.done]
when = "hvb:state:merged"

[columns.canceled]
when = "hvb:state:canceled"

[repos."aryrabelo/bugtoprompt"]
quiet_timer = "30m"
`

func loadProject(t *testing.T, content string) *Config {
	t.Helper()
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, content)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("the contract's own configuration must load: %v", err)
	}
	return cfg
}

// The twelve declared columns are the board, in the declared order, with the
// headings and the transitions the file asked for.
func TestBoardWorkflowDrawsTheDeclaredLine(t *testing.T) {
	cfg := loadProject(t, contractLine)
	wf, err := cfg.BoardWorkflow()
	if err != nil {
		t.Fatal(err)
	}
	want := []feature.Status{"intake", "triage", "planning", "building", "first-review",
		"fr-approved", "pr-processing", "pr-verification", "ready-to-review",
		"ready-to-merge", "done", "canceled"}
	got := wf.Columns()
	if len(got) != len(want) {
		t.Fatalf("the board has %d columns, want the %d declared: %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("column %d is %q, want %q", index, got[index], want[index])
		}
	}
	if title := wf.Title("ready-to-review"); title != "Ready To Review" {
		t.Errorf("declared title lost: %q", title)
	}
	if short := wf.Short("first-review"); short != "FR" {
		t.Errorf("declared abbreviation lost: %q", short)
	}
	if !wf.HumanGate("ready-to-review") {
		t.Error(`gate = "human" did not reach the workflow`)
	}
	if wf.HumanGate("building") {
		t.Error("a column with no gate must not be a human gate")
	}
	if next := wf.Next("pr-verification"); len(next) != 2 || next[0] != "building" || next[1] != "ready-to-review" {
		t.Errorf("pr-verification next = %v, want [building ready-to-review] in declared order", next)
	}
	if !wf.Terminal("done") {
		t.Error("a column declaring no next must be terminal")
	}
	// Nothing on a declared line hides: all twelve are drawn empty.
	for _, column := range got {
		if wf.HideWhenEmpty(column) {
			t.Errorf("declared column %q hides while empty; only the synthesized tail does", column)
		}
	}
}

// The `when` labels and the quiet destination are what the line policy reads.
func TestDeclaredLineExposesItsLabelsAndQuietColumn(t *testing.T) {
	cfg := loadProject(t, contractLine)
	when := cfg.LineWhen()
	for column, label := range map[string]string{
		"pr-verification": "hvb:state:open",
		"done":            "hvb:state:merged",
		"canceled":        "hvb:state:canceled",
	} {
		if got := when[column]; got != label {
			t.Errorf("LineWhen()[%q] = %q, want %q", column, got, label)
		}
	}
	if got := when["building"]; got != "" {
		t.Errorf("a column that declared no when claims %q", got)
	}
	if got := cfg.LineQuiet(); got != "ready-to-review" {
		t.Errorf("LineQuiet() = %q, want ready-to-review", got)
	}
}

// The default board is today's: the five vb columns plus the cancelled tail,
// hidden until something lands in it.
//
// The label half is asserted in the same test as the column half on purpose.
// They are one decision: a synthesized column with no synthesized label is a
// column no card can reach, which puts a cancelled pull request back in Done.
func TestDefaultBoardKeepsTheCancelledTailAndItsLabel(t *testing.T) {
	cfg := Default()
	wf, err := cfg.BoardWorkflow()
	if err != nil {
		t.Fatal(err)
	}
	want := []feature.Status{feature.Backlog, feature.InProgress, feature.Blocked,
		feature.Review, feature.Done, "canceled"}
	got := wf.Columns()
	if len(got) != len(want) {
		t.Fatalf("the default board has %d columns, want %d: %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("column %d is %q, want %q", index, got[index], want[index])
		}
	}
	if !wf.HideWhenEmpty("canceled") {
		t.Error("the cancelled tail must stay off a board that has nothing cancelled")
	}
	for _, column := range want[:5] {
		if wf.HideWhenEmpty(column) {
			t.Errorf("vb column %q must always be drawn", column)
		}
	}
	if next := wf.Next(feature.Review); len(next) != 2 || next[0] != feature.InProgress || next[1] != feature.Done {
		t.Errorf("the default board lost vb's transitions: review → %v", next)
	}
	if got := cfg.LineWhen()["canceled"]; got != "hvb:state:canceled" {
		t.Fatalf("LineWhen()[canceled] = %q; the synthesized column has no label to reach it, so a cancelled pull request would sit in Done", got)
	}
}

// A quiet window is measured in the minutes a check takes and the hours a human
// waits, so both ends are refused and the refusal names both.
func TestQuietTimerRefusesAWindowOutsideItsBounds(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	for _, window := range []string{"5m", "48h"} {
		root := t.TempDir()
		writeConfig(t, root, ProjectFile,
			"[repos.\"aryrabelo/bugtoprompt\"]\nquiet_timer = \""+window+"\"\n")
		_, err := Load(root)
		if err == nil {
			t.Fatalf("quiet_timer = %q is outside 10m–24h and must be refused", window)
		}
		for _, want := range []string{"10m", "24h", "aryrabelo/bugtoprompt", window} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("quiet_timer = %q: error %q does not name %q", window, err, want)
			}
		}
	}
	cfg := loadProject(t, "[repos.\"aryrabelo/bugtoprompt\"]\nquiet_timer = \"30m\"\n")
	window, ok := cfg.QuietTimer("aryrabelo/bugtoprompt")
	if !ok || window != 30*time.Minute {
		t.Errorf("QuietTimer = %s, %v; want 30m, true", window, ok)
	}
}

// A repository that declared no window never advances on its own, and the bool
// is how the policy knows the difference between that and a zero window.
func TestQuietTimerReportsAbsenceRatherThanADefault(t *testing.T) {
	cfg := loadProject(t, contractLine)
	if window, ok := cfg.QuietTimer("aryrabelo/somewhere-else"); ok || window != 0 {
		t.Errorf("QuietTimer of an undeclared repository = %s, %v; want 0, false", window, ok)
	}
}

// A quiet green pull request has one destination. Two columns claiming it is a
// configuration with no answer, not a race to be resolved at render time.
func TestValidateRefusesTwoQuietColumns(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, `
[workflow]
columns = ["intake", "ready-to-review", "ready-to-merge"]

[columns.ready-to-review]
quiet = true

[columns.ready-to-merge]
quiet = true
`)
	_, err := Load(root)
	if err == nil {
		t.Fatal("two columns declaring quiet = true must be refused")
	}
	for _, want := range []string{"ready-to-review", "ready-to-merge", "quiet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// The vb-authority gate is separate from Load's, and this is the difference:
// `in-progress → done` is a perfectly good edge on a declared line and an
// illegal move on the spec board, so the same file loads and the spec board
// refuses it, naming vb.
func TestValidateSpecBoardRefusesANextVBDoesNotAllow(t *testing.T) {
	cfg := loadProject(t, `
[workflow]
columns = ["backlog", "in-progress", "blocked", "review", "done"]

[columns.in-progress]
next = ["done"]
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the board-independent gate must accept a declared line: %v", err)
	}
	err := cfg.ValidateSpecBoard()
	if err == nil {
		t.Fatal("in-progress → done is not a vb transition and the spec board must refuse it")
	}
	for _, want := range []string{"in-progress", "done", "vb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// The other half of that gate: a column vb has never heard of is fine on a
// queue board and refused on the spec board.
func TestValidateSpecBoardRefusesAColumnVBDoesNotKnow(t *testing.T) {
	cfg := loadProject(t, contractLine)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the contract's line must load: %v", err)
	}
	err := cfg.ValidateSpecBoard()
	if err == nil {
		t.Fatal("the spec board must refuse a column that is not a vb status")
	}
	if !strings.Contains(err.Error(), "vb") {
		t.Errorf("error %q does not name vb as the authority", err)
	}
}

func TestDefaultPassesTheSpecBoardGate(t *testing.T) {
	defaults := Default()
	if err := defaults.ValidateSpecBoard(); err != nil {
		t.Fatalf("the built-in pipeline must be a legal vb board: %v", err)
	}
}

// A declared line must be self-contained: a table describing a column the line
// does not draw, or a move leaving the line, is a typo the reader has to see.
func TestValidateRefusesADeclaredLineThatDoesNotHoldTogether(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	for name, content := range map[string]string{
		"a table off the line": `
[workflow]
columns = ["intake", "done"]

[columns.building]
auto = true
`,
		"a move off the line": `
[workflow]
columns = ["intake", "done"]

[columns.intake]
next = ["building"]
`,
		"a name that is not a slug": `
[workflow]
columns = ["intake", "Ready To Review"]
`,
		"the same column twice": `
[workflow]
columns = ["intake", "intake"]
`,
	} {
		root := t.TempDir()
		writeConfig(t, root, ProjectFile, content)
		if _, err := Load(root); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// quiet_timer decides nothing about what hvb executes or who it authenticates
// to, so a repository may declare its own. The operator-only list is for the
// keys that pick a program or a host; putting a window in it would mean the
// only person who can say how long a pull request must go quiet is whoever
// installed hvb.
func TestAProjectFileMayDeclareItsOwnQuietTimer(t *testing.T) {
	cfg := loadProject(t, "[repos.\"aryrabelo/bugtoprompt\"]\nquiet_timer = \"45m\"\n")
	if window, ok := cfg.QuietTimer("aryrabelo/bugtoprompt"); !ok || window != 45*time.Minute {
		t.Fatalf("QuietTimer = %s, %v; a repository file must be allowed to set it", window, ok)
	}
}

// A gate is a person or nothing. Anything else is a word someone expected to
// mean something, and the board would silently treat it as no gate at all.
func TestValidateRefusesAGateThatIsNotHuman(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, "[columns.review]\ngate = \"robot\"\n")
	_, err := Load(root)
	if err == nil {
		t.Fatal(`gate = "robot" must be refused`)
	}
	for _, want := range []string{"review", "robot", "human"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// Loading and opening the board must agree about the same file. An empty entry
// in `next` used to pass Validate — routesOf skips it — and then reach
// feature.NewWorkflow as a move to "", so the file loaded and the board
// refused to start with a dangling-column error naming nothing. The refusal
// belongs at the file that has the typo.
func TestValidateRefusesAnEmptyNextEntry(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, `
[workflow]
columns = ["intake", "done"]

[columns.intake]
next = ["done", ""]
`)
	cfg, err := Load(root)
	if err == nil {
		// The proof that this is one disagreement and not two
		// behaviours: the file that loaded cannot draw a board.
		_, boardErr := cfg.BoardWorkflow()
		t.Fatalf(`next = ["done", ""] loaded, and BoardWorkflow then said: %v`, boardErr)
	}
	for _, want := range []string{"intake", "next"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// A queue line may reuse a built-in column name: "review" and "done" are
// ordinary words for a board of pull requests. Doing so must not import the
// built-in pipeline's routes — Default() sends review to done on success and
// back to in-progress on failure — because the file declared neither. The line
// below drew review and done without in-progress and was refused over a move
// nobody wrote, whose only workaround was to spell `on_failure = ""` for a
// column the file never mentions.
func TestADeclaredLineDoesNotInheritBuiltInRoutes(t *testing.T) {
	cfg := loadProject(t, `
[workflow]
columns = ["intake", "review", "done"]
`)
	wf, err := cfg.BoardWorkflow()
	if err != nil {
		t.Fatalf("BoardWorkflow: %v", err)
	}
	want := []feature.Status{"intake", "review", "done"}
	got := wf.Columns()
	if len(got) != len(want) {
		t.Fatalf("the board draws %v, want %v", got, want)
	}
	for index, column := range want {
		if got[index] != column {
			t.Fatalf("the board draws %v, want %v", got, want)
		}
	}
	// Inherited and ignored, not inherited and drawn: nothing moves off
	// review, because no file said anything about review.
	if next := wf.Next("review"); len(next) != 0 {
		t.Errorf("review routes to %v; a name reused from the built-in pipeline declares no moves", next)
	}
}

// The same rule from the other side: a table the FILE wrote is still held to
// the line, so a real typo is still named.
func TestAFileWrittenRouteOffTheLineIsStillRefused(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, `
[workflow]
columns = ["intake", "review", "done"]

[columns.review]
on_failure = "in-progress"
`)
	if _, err := Load(root); err == nil {
		t.Fatal(`on_failure = "in-progress" off a three-column line was accepted`)
	}
}
