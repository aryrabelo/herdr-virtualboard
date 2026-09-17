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
	// LabelGrilling marks a decision under examination, not work in flight.
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
//	rumo:grilling   -> Review      (outranks assignee)
//	assigned        -> InProgress
//	otherwise       -> Backlog
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
	if hasLabel(labels, LabelGrilling) {
		return Review
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
