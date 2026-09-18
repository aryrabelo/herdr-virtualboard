// Package feature models VirtualBoard feature specs: the frontmatter, the body
// sections, and the lifecycle graph the specs move through.
package feature

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Status is one column of a lifecycle: one of VirtualBoard's five states on the
// spec board, or a declared column on a configured one. The zero value is not a
// valid status; use Workflow.Parse to obtain one from user input.
type Status string

const (
	Backlog    Status = "backlog"
	InProgress Status = "in-progress"
	Blocked    Status = "blocked"
	Review     Status = "review"
	Done       Status = "done"
)

// String renders the status as it appears in frontmatter and directory names.
func (s Status) String() string { return string(s) }

// Title renders the status for column headers: kebab case becomes Title Case,
// so `in-progress` reads as "In Progress" and `first-review` as "First Review".
//
// It is a derivation rather than a table because a configured board declares
// columns this package has never heard of, and a table would render them as the
// lower-case slug next to five properly capitalised neighbours.
func (s Status) Title() string {
	parts := strings.Split(string(s), "-")
	for index, part := range parts {
		parts[index] = capitalise(part)
	}
	return strings.Join(parts, " ")
}

// Dir returns the feature directory for the status, relative to the
// VirtualBoard workspace root.
func (s Status) Dir() string { return "features/" + string(s) }

// ColumnDef declares one column of a Workflow.
type ColumnDef struct {
	// Name is the column's slug, as it appears in configuration and in a
	// spec's `status` frontmatter.
	Name Status
	// Next are the columns a card here may move to, in the order a picker
	// offers them. Empty makes the column terminal.
	Next []Status
	// HumanGate marks a column work stops in until a person moves it on.
	HumanGate bool
	// Title is the column header. Empty derives from Name.
	Title string
	// Short is the abbreviation the header counts and the breadcrumb use.
	// Empty derives from Name.
	Short string
	// HideWhenEmpty keeps the column off the board until something lands in
	// it. It exists for a tail column the ordinary board never reaches — a
	// cancelled pull request — which should not cost a sixth of the width
	// on every board that has none.
	HideWhenEmpty bool
}

// Workflow is an ordered lifecycle: the columns a board shows and the
// transitions each one allows.
//
// A Workflow is data, not authority. VB() duplicates vb-cli's own transition
// table so the TUI can grey out impossible moves before shelling out, but vb
// remains the enforcing authority: every move on the spec board still goes
// through `vb move`, which rejects anything this table would have allowed in
// error.
type Workflow struct {
	order []Status
	defs  map[Status]ColumnDef
	at    map[Status]int
}

// NewWorkflow builds a lifecycle from declared columns, in board order.
//
// A dangling `next` is refused rather than ignored: a column offering a move to
// somewhere the board does not draw is a picker entry that can only fail, and
// the failure would surface as a vb error long after the configuration was
// written.
func NewWorkflow(defs []ColumnDef) (*Workflow, error) {
	if len(defs) == 0 {
		return nil, errors.New("workflow: no columns declared (a board needs at least one)")
	}
	w := &Workflow{
		order: make([]Status, 0, len(defs)),
		defs:  make(map[Status]ColumnDef, len(defs)),
		at:    make(map[Status]int, len(defs)),
	}
	for _, def := range defs {
		if strings.TrimSpace(string(def.Name)) == "" {
			return nil, errors.New(`workflow: a column has no name (every column needs its slug, e.g. "first-review")`)
		}
		if _, duplicate := w.defs[def.Name]; duplicate {
			return nil, fmt.Errorf("workflow: column %q is declared twice (each column appears once, in board order)", def.Name)
		}
		w.at[def.Name] = len(w.order)
		w.order = append(w.order, def.Name)
		w.defs[def.Name] = def
	}
	// Checked after every column is registered, so a column may name one
	// declared to its left or to its right.
	for _, def := range defs {
		for _, next := range def.Next {
			if _, ok := w.defs[next]; !ok {
				return nil, fmt.Errorf("workflow: column %q may move to %q, which this workflow does not declare (declare %q as a column or drop it from next)",
					def.Name, next, next)
			}
		}
	}
	return w, nil
}

// vbWorkflow is built once. The table is a literal in this file, so an error
// here is a programming mistake rather than bad input, and every test in this
// package would hit it.
var vbWorkflow = mustWorkflow([]ColumnDef{
	{Name: Backlog, Next: []Status{InProgress}, Short: "BACK"},
	{Name: InProgress, Next: []Status{Blocked, Review}, Short: "WIP"},
	{Name: Blocked, Next: []Status{InProgress}, Short: "BLOCK"},
	{Name: Review, Next: []Status{InProgress, Done}, Short: "REVIEW"},
	{Name: Done, Short: "DONE"},
})

func mustWorkflow(defs []ColumnDef) *Workflow {
	w, err := NewWorkflow(defs)
	if err != nil {
		panic(err)
	}
	return w
}

// VB is the vb-cli lifecycle: the five states and their transitions. It is the
// authority on the spec board and never reads configuration.
func VB() *Workflow { return vbWorkflow }

// Columns lists the lifecycle in board order, left to right. The slice is the
// workflow's own — a board asks for it on every frame, so copying it would
// allocate per redraw — and callers only read it.
func (w *Workflow) Columns() []Status { return w.order }

// Has reports whether s is a column of this workflow.
func (w *Workflow) Has(s Status) bool {
	_, ok := w.defs[s]
	return ok
}

// Parse resolves a column name case-insensitively, tolerating the underscore
// spelling some shells produce from directory names.
func (w *Workflow) Parse(s string) (Status, bool) {
	normalized := Status(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "_", "-"))
	if !w.Has(normalized) {
		return "", false
	}
	return normalized, true
}

// Next returns the columns s may move to, in declared order.
func (w *Workflow) Next(s Status) []Status { return w.defs[s].Next }

// CanTransition reports whether current may move directly to target.
func (w *Workflow) CanTransition(current, target Status) bool {
	for _, next := range w.defs[current].Next {
		if next == target {
			return true
		}
	}
	return false
}

// Terminal reports whether no transition leaves this column. `done` is terminal
// by VirtualBoard rule: a feature never moves out of it.
func (w *Workflow) Terminal(s Status) bool { return len(w.defs[s].Next) == 0 }

// HumanGate reports whether work stops in this column until a person moves it.
func (w *Workflow) HumanGate(s Status) bool { return w.defs[s].HumanGate }

// Title is the column header: what the column declared, else derived from its
// name.
func (w *Workflow) Title(s Status) string {
	if title := w.defs[s].Title; title != "" {
		return title
	}
	return s.Title()
}

// Short is the column abbreviation: what the column declared, else derived.
//
// The derivation has two shapes because one rule reads badly for both: a single
// word cut to six columns stays recognisable (`intake` → `INTAKE`), while a
// multi-word name cut the same way loses the part that distinguishes it
// (`ready-to-review` and `ready-to-merge` would both be `READY-`), so those
// become initials instead.
func (w *Workflow) Short(s Status) string {
	if short := w.defs[s].Short; short != "" {
		return short
	}
	parts := strings.Split(string(s), "-")
	if len(parts) == 1 {
		word := strings.ToUpper(parts[0])
		if len(word) > shortWidth {
			return word[:shortWidth]
		}
		return word
	}
	var initials strings.Builder
	for _, part := range parts {
		for _, r := range part {
			initials.WriteRune(unicode.ToUpper(r))
			break
		}
	}
	return initials.String()
}

// shortWidth is how much of a single-word column name the abbreviation keeps.
// It is six because the header draws the count beside it in a cell a twelve
// column board gives about twenty-two columns to.
const shortWidth = 6

// HideWhenEmpty reports whether the column stays off the board while empty.
func (w *Workflow) HideWhenEmpty(s Status) bool { return w.defs[s].HideWhenEmpty }

// Index is the column's position in board order, or -1 when the workflow does
// not declare it.
func (w *Workflow) Index(s Status) int {
	if at, ok := w.at[s]; ok {
		return at
	}
	return -1
}

func capitalise(word string) string {
	for index, r := range word {
		return string(unicode.ToUpper(r)) + word[index+len(string(r)):]
	}
	return word
}
