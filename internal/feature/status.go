// Package feature models VirtualBoard feature specs: the frontmatter, the body
// sections, and the lifecycle graph the specs move through.
package feature

import "strings"

// Status is one of VirtualBoard's five lifecycle states. The zero value is not
// a valid status; use ParseStatus to obtain one from user input.
type Status string

const (
	Backlog    Status = "backlog"
	InProgress Status = "in-progress"
	Blocked    Status = "blocked"
	Review     Status = "review"
	Done       Status = "done"
)

// Statuses lists the lifecycle in board order, left to right. The TUI renders
// one column per entry and `hvb feature list` groups by it.
var Statuses = []Status{Backlog, InProgress, Blocked, Review, Done}

// allowedTransitions mirrors internal/feature/status.go in vb-cli. It is
// duplicated here so the TUI can grey out impossible moves before shelling out,
// but vb remains the enforcing authority: every move still goes through
// `vb move`, which rejects anything this table would have allowed in error.
var allowedTransitions = map[Status][]Status{
	Backlog:    {InProgress},
	InProgress: {Blocked, Review},
	Blocked:    {InProgress},
	Review:     {InProgress, Done},
	Done:       {},
}

// ParseStatus resolves a status name case-insensitively, tolerating the
// underscore spelling some shells produce from directory names.
func ParseStatus(s string) (Status, bool) {
	normalized := Status(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "_", "-"))
	if _, ok := allowedTransitions[normalized]; !ok {
		return "", false
	}
	return normalized, true
}

// Valid reports whether s is one of the five lifecycle states.
func (s Status) Valid() bool {
	_, ok := allowedTransitions[s]
	return ok
}

// String renders the status as it appears in frontmatter and directory names.
func (s Status) String() string { return string(s) }

// Title renders the status for column headers.
func (s Status) Title() string {
	switch s {
	case InProgress:
		return "In Progress"
	default:
		return strings.ToUpper(string(s)[:1]) + string(s)[1:]
	}
}

// Dir returns the feature directory for the status, relative to the
// VirtualBoard workspace root.
func (s Status) Dir() string { return "features/" + string(s) }

// Terminal reports whether no transition leaves this status. `done` is terminal
// by VirtualBoard rule: a feature never moves out of it.
func (s Status) Terminal() bool { return len(allowedTransitions[s]) == 0 }

// NextStatuses returns the statuses s may transition to, in board order.
func (s Status) NextStatuses() []Status {
	allowed := allowedTransitions[s]
	out := make([]Status, 0, len(allowed))
	for _, candidate := range Statuses {
		for _, next := range allowed {
			if candidate == next {
				out = append(out, candidate)
			}
		}
	}
	return out
}

// CanTransition reports whether current may move directly to target.
func CanTransition(current, target Status) bool {
	for _, next := range allowedTransitions[current] {
		if next == target {
			return true
		}
	}
	return false
}
