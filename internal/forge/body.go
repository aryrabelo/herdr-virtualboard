package forge

import (
	"fmt"
	"strings"
)

// BodyInput is what a pull-request description is built from.
type BodyInput struct {
	FeatureID  string
	Title      string
	Status     string
	SpecPath   string
	Summary    string
	Criteria   []Criterion
	Commits    []string
	Role       string
	Harness    string
	RunID      string
	Attributed bool
}

// Criterion is one acceptance-criteria line.
type Criterion struct {
	Text string
	Done bool
}

// Body composes the pull-request description.
//
// It is written for the human reviewing the branch, not for the board: the
// acceptance criteria come first because they are what the reviewer is checking
// against, and the provenance goes last because it matters only if something
// looks wrong.
func Body(in BodyInput) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Implements **%s — %s**.\n\n", in.FeatureID, in.Title)

	if summary := strings.TrimSpace(in.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}

	if len(in.Criteria) > 0 {
		b.WriteString("## Acceptance criteria\n\n")
		for _, criterion := range in.Criteria {
			box := " "
			if criterion.Done {
				box = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s\n", box, criterion.Text)
		}
		b.WriteString("\n")
	}

	if len(in.Commits) > 0 {
		b.WriteString("## Commits\n\n")
		for _, subject := range in.Commits {
			fmt.Fprintf(&b, "- %s\n", subject)
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n\n")
	if in.SpecPath != "" {
		fmt.Fprintf(&b, "Specification: `%s`\n", in.SpecPath)
	}
	if in.Role != "" {
		fmt.Fprintf(&b, "Dispatched by herdr-virtualboard as `%s`", in.Role)
		if in.Harness != "" {
			fmt.Fprintf(&b, " on `%s`", in.Harness)
		}
		b.WriteString(".\n")
	}
	return b.String()
}
