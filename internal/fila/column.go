package fila

import "strings"

// Column is a board lane.
type Column string

const (
	Backlog    Column = "backlog"
	InProgress Column = "in-progress"
	Blocked    Column = "blocked"
	Review     Column = "review"
	Done       Column = "done"
)

const (
	// LabelHITL marks work only the owner can unblock ("Bloqueio que só o
	// dono resolve"). It is literally a block on a human, which is why it
	// outranks an assignee: every HITL issue in the owner's queue is
	// already assigned to him and is still stuck.
	LabelHITL = "hitl"
	// LabelGrilling marks a decision under examination. It is a DECISION
	// KIND, not a lifecycle state, and it deliberately decides no column —
	// see ColumnFor. It is exported because internal/roles maps it to the
	// charter an agent adopts, which is the axis this label belongs to.
	LabelGrilling = "rumo:grilling"
)

// ColumnFor decides a card's column from facts the card already carries. Pure
// policy: no I/O, no package state, nothing to stub.
//
// The precedence is fixed and total, and the order is the point:
//
//	closed          -> Done        (before any label; a closed HITL is done)
//	label hitl      -> Blocked     (outranks assignee)
//	frontierBlocked -> Blocked
//	assigned        -> InProgress
//	otherwise       -> Backlog
//
// Only a label that names a STATE may decide a column. `hitl` qualifies: it
// says the card is stuck on a human, which is why it outranks an assignee.
// The owner's `rumo:*` labels do not, and one of them used to: `rumo:grilling`
// mapped to Review above `assigned`, which put 7 of his 33 cards in REVIEW
// with no pull request anywhere — 6 of the 7 had none at all (measured
// 2026-09-18), because nothing here reads a PR. His own convention
// (`specs/usina-fonte-unica.md`) separates the two axes by name: `estágio` is
// ideia/spec/folha, `decisão` is rumo:research|grilling|prototype|task|projeto.
// Reading a decision KIND as a lifecycle STATE also let a grilling card move
// review -> done without ever having been built. The kind still shows on the
// card's label line and still picks the agent's charter (internal/roles),
// which is the axis that asks "what kind of work is this".
func ColumnFor(closed bool, labels []string, assigned bool, frontierBlocked bool) Column {
	if closed {
		return Done
	}
	if hasLabel(labels, LabelHITL) {
		return Blocked
	}
	if frontierBlocked {
		return Blocked
	}
	if assigned {
		return InProgress
	}
	return Backlog
}

// hasLabel compares the way a human reads a label: surrounding space and
// casing are noise, the name is the signal.
func hasLabel(labels []string, want string) bool {
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label), want) {
			return true
		}
	}
	return false
}
