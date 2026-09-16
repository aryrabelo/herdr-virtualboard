package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/netors/herdr-virtualboard/internal/feature"
	"github.com/netors/herdr-virtualboard/internal/runs"
)

// renderFeatureTable prints the list grouped by status, in board order.
func renderFeatureTable(app *App, rows []featureRow) {
	byStatus := map[string][]featureRow{}
	for _, row := range rows {
		byStatus[row.Status] = append(byStatus[row.Status], row)
	}
	first := true
	for _, status := range feature.Statuses {
		group := byStatus[string(status)]
		if len(group) == 0 {
			continue
		}
		if !first {
			app.Print("")
		}
		first = false
		app.Print("%s (%d)", strings.ToUpper(status.Title()), len(group))
		for _, row := range group {
			marker := " "
			if row.ActiveRun != "" {
				marker = "▶"
			}
			owner := ""
			if row.Owner != "" && row.Owner != feature.Unassigned {
				owner = "  @" + row.Owner
			}
			labels := ""
			if len(row.Labels) > 0 {
				labels = "  [" + strings.Join(row.Labels, " ") + "]"
			}
			app.Print("  %s %-9s %-3s %s%s%s", marker, row.ID, dash(row.Priority), row.Title, owner, labels)
		}
	}
}

// renderFeatureDetail prints one spec: header, criteria, sections, run history.
func renderFeatureDetail(app *App, spec *feature.Spec, history []*runs.Run) {
	app.Print("%s  %s", spec.ID, spec.Title)
	app.Print("%s", strings.Repeat("─", 60))
	app.Print("status      %s", spec.Status)
	app.Print("owner       %s", dash(spec.Owner))
	app.Print("priority    %s", dash(spec.Priority))
	app.Print("complexity  %s", dash(spec.Complexity))
	app.Print("created     %s", dash(spec.Created))
	app.Print("updated     %s", dash(spec.Updated))
	if len(spec.Labels) > 0 {
		app.Print("labels      %s", strings.Join(spec.Labels, ", "))
	}
	if len(spec.Dependencies) > 0 {
		app.Print("depends on  %s", strings.Join(spec.Dependencies, ", "))
	}
	if spec.Epic != "" {
		app.Print("epic        %s", spec.Epic)
	}
	if strings.TrimSpace(spec.RiskNotes) != "" {
		app.Print("risk        %s", spec.RiskNotes)
	}

	if criteria := spec.AcceptanceCriteria(); len(criteria) > 0 {
		app.Print("")
		done := 0
		for _, criterion := range criteria {
			if criterion.Done {
				done++
			}
		}
		app.Print("Acceptance criteria (%d/%d)", done, len(criteria))
		for _, criterion := range criteria {
			box := " "
			if criterion.Done {
				box = "x"
			}
			app.Print("  [%s] %s", box, criterion.Text)
		}
	}

	if summary, ok := spec.Section("Summary"); ok && summary != "" {
		app.Print("")
		app.Print("Summary")
		for _, line := range wrap(summary, 76) {
			app.Print("  %s", line)
		}
	}

	if len(history) > 0 {
		app.Print("")
		app.Print("Runs (%d)", len(history))
		for _, run := range history {
			app.Print("  %s", formatRunLine(run))
			for _, comment := range run.Comments {
				app.Print("      %s %s: %s", comment.At.Local().Format("15:04"), comment.Author, firstLine(comment.Body))
			}
		}
	}
}

// formatRunLine renders one run as a single line for lists and detail views.
func formatRunLine(run *runs.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-7s %-9s %-12s %-10s %s",
		stateGlyph(run.State), run.FeatureID, run.State, run.Role, formatDuration(run.Duration()))
	if run.Outcome != "" {
		fmt.Fprintf(&b, "  (%s)", run.Outcome)
	}
	if run.MovedTo != "" {
		fmt.Fprintf(&b, " → %s", run.MovedTo)
	}
	return b.String()
}

func stateGlyph(state runs.State) string {
	switch state {
	case runs.Running:
		return "▶"
	case runs.Queued:
		return "·"
	case runs.Awaiting:
		return "?"
	case runs.Succeeded:
		return "✓"
	case runs.Failed:
		return "✗"
	case runs.Cancelled:
		return "⊘"
	default:
		return " "
	}
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index] + " …"
	}
	return text
}

// wrap breaks text into lines of at most width runes, on word boundaries.
func wrap(text string, width int) []string {
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if len([]rune(line))+1+len([]rune(word)) > width {
				out = append(out, line)
				line = word
				continue
			}
			line += " " + word
		}
		out = append(out, line)
	}
	return out
}
