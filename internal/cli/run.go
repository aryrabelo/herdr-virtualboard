package cli

import (
	"os"
	"strings"

	"github.com/netors/herdr-virtualboard/internal/dispatch"
	"github.com/netors/herdr-virtualboard/internal/runs"
	"github.com/spf13/cobra"
)

func newRunCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "run",
		Aliases: []string{"r"},
		Short:   "Dispatch agents and report their outcomes",
	}
	cmd.AddCommand(
		newRunStartCommand(app),
		newRunListCommand(app),
		newRunShowCommand(app),
		newRunDoneCommand(app),
		newRunCommentCommand(app),
		newRunCancelCommand(app),
		newRunFocusCommand(app),
		newRunLogCommand(app),
	)
	return cmd
}

func newRunStartCommand(app *App) *cobra.Command {
	var (
		role  string
		kind  string
		focus bool
		force bool
	)
	cmd := &cobra.Command{
		Use:   "start <FEATURE-ID>",
		Short: "Dispatch a VirtualBoard role agent onto a feature",
		Long: `Dispatch a VirtualBoard role agent onto a feature.

hvb claims the feature's vb lock, opens or reuses the feature's Herdr tab,
splits a pane with the run's environment, starts the harness there, and submits
the composed prompt. The role is inferred from the feature's labels and status
unless --role says otherwise.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			spec, err := app.Workspace().FindSpec(args[0])
			if err != nil {
				return err
			}
			run, err := app.Dispatcher().Start(contextFor(cmd), dispatch.Request{
				Spec: spec, Role: role, Kind: kind, Focus: focus, Force: force,
			})
			if err != nil {
				return err
			}
			if app.Emit(run) {
				return nil
			}
			app.Print("Dispatched %s as %s (%s)", run.FeatureID, run.Role, run.Kind)
			app.Print("  run   %s", run.ID)
			app.Print("  pane  %s", run.PaneID)
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "VirtualBoard role charter to adopt (see `hvb role list`)")
	cmd.Flags().StringVar(&kind, "harness", "", "Herdr agent kind (see `hvb harness list`)")
	cmd.Flags().BoolVar(&focus, "focus", false, "switch to the new agent pane")
	cmd.Flags().BoolVar(&force, "force", false, "take the feature lock even if another owner holds it")
	return cmd
}

func newRunListCommand(app *App) *cobra.Command {
	var (
		all       bool
		featureID string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List runs, newest first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			// Reconcile first: a list that reports a run as running when its
			// pane closed an hour ago is worse than no list at all.
			if _, err := app.Dispatcher().Reconcile(contextFor(cmd)); err != nil {
				app.Warn("hvb: %v", err)
			}
			var (
				found []*runs.Run
				err   error
			)
			switch {
			case featureID != "":
				found, err = app.Store().ForFeature(featureID)
			case all:
				found, err = app.Store().List()
			default:
				found, err = app.Store().Active()
			}
			if err != nil {
				return err
			}
			if app.Emit(map[string]any{"runs": found, "total": len(found)}) {
				return nil
			}
			if len(found) == 0 {
				if all || featureID != "" {
					app.Print("No runs.")
				} else {
					app.Print("No active runs. Pass --all for history.")
				}
				return nil
			}
			for _, run := range found {
				app.Print("%s  %s", formatRunLine(run), run.ID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include finished runs")
	cmd.Flags().StringVar(&featureID, "feature", "", "only runs for this feature")
	return cmd
}

func newRunShowCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show [RUN-ID]",
		Short: "Show one run",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			// Reconcile first, for the same reason `run list` does: a run
			// reported as running when its pane closed an hour ago is worse
			// than no answer.
			if _, err := app.Dispatcher().Reconcile(contextFor(cmd)); err != nil {
				app.Warn("hvb: %v", err)
			}
			run, err := resolveRun(app, args)
			if err != nil {
				return err
			}
			if app.Emit(run) {
				return nil
			}
			app.Print("run       %s", run.ID)
			app.Print("feature   %s — %s", run.FeatureID, run.Title)
			app.Print("state     %s", run.State)
			app.Print("role      %s (%s)", run.Role, run.Kind)
			app.Print("started   %s", run.StartedAt.Local().Format("2006-01-02 15:04:05"))
			app.Print("duration  %s", formatDuration(run.Duration()))
			if run.PaneID != "" {
				app.Print("pane      %s (agent %s)", run.PaneID, run.AgentName)
			}
			if run.Outcome != "" {
				app.Print("outcome   %s", run.Outcome)
			}
			if run.MovedTo != "" {
				app.Print("moved to  %s", run.MovedTo)
			}
			if run.Note != "" {
				app.Print("note      %s", run.Note)
			}
			for _, problem := range run.LastErrors {
				app.Warn("  ! %s", problem)
			}
			if len(run.Comments) > 0 {
				app.Print("")
				app.Print("Comments (%d)", len(run.Comments))
				for _, comment := range run.Comments {
					app.Print("  %s %s", comment.At.Local().Format("15:04:05"), comment.Author)
					for _, line := range wrap(comment.Body, 74) {
						app.Print("    %s", line)
					}
				}
			}
			return nil
		},
	}
	return cmd
}

func newRunDoneCommand(app *App) *cobra.Command {
	var (
		outcome string
		note    string
	)
	cmd := &cobra.Command{
		Use:   "done [RUN-ID]",
		Short: "Report a run's outcome and let the board route the feature",
		Long: `Report a run's outcome.

This is the call a dispatched agent makes exactly once, at the end of its work.
Without a run id it uses $HVB_RUN_ID. The board then applies the column's
routing: success and failure each move the feature to a configured status, and
a blocked outcome moves it to blocked where the lifecycle allows.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			parsed, ok := dispatch.ParseOutcome(outcome)
			if !ok {
				return Usage("unknown outcome %q (expected success, failure, or blocked)", outcome)
			}
			run, err := resolveRun(app, args)
			if err != nil {
				return err
			}
			completion, err := app.Dispatcher().Complete(contextFor(cmd), run.ID, parsed, note)
			if err != nil {
				return err
			}
			if app.Emit(completion) {
				return nil
			}
			app.Print("%s reported %s", completion.Run.FeatureID, completion.Run.Outcome)
			if completion.Moved != "" {
				app.Print("  moved to %s", completion.Moved)
			}
			if completion.TransitionError != "" {
				app.Warn("hvb: the feature was not moved: %s", completion.TransitionError)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outcome, "outcome", "success", "success, failure, or blocked")
	cmd.Flags().StringVar(&note, "note", "", "why — required in practice for failure and blocked")
	return cmd
}

func newRunCommentCommand(app *App) *cobra.Command {
	var author string
	cmd := &cobra.Command{
		Use:   "comment <text> [RUN-ID]",
		Short: "Attach a progress note to a run",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			run, err := resolveRun(app, args[1:])
			if err != nil {
				return err
			}
			if author == "" {
				author = strings.TrimSpace(os.Getenv("HVB_ROLE"))
			}
			if author == "" {
				author = app.Config().ResolveOwner()
			}
			updated, err := app.Store().AddComment(run.ID, author, args[0])
			if err != nil {
				return err
			}
			if app.Emit(updated) {
				return nil
			}
			app.Print("Comment added to %s", updated.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&author, "author", "", "comment author (default: $HVB_ROLE, then the configured owner)")
	return cmd
}

func newRunCancelCommand(app *App) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "cancel [RUN-ID]",
		Short: "Stop a run and close its pane",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			run, err := resolveRun(app, args)
			if err != nil {
				return err
			}
			cancelled, err := app.Dispatcher().Cancel(contextFor(cmd), run.ID, note)
			if err != nil {
				return err
			}
			if app.Emit(cancelled) {
				return nil
			}
			app.Print("Cancelled %s (%s)", cancelled.ID, cancelled.FeatureID)
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "why the run was cancelled")
	return cmd
}

func newRunFocusCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "focus [RUN-ID]",
		Short: "Switch to a run's agent pane",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			run, err := resolveRun(app, args)
			if err != nil {
				return err
			}
			if err := app.Dispatcher().Focus(contextFor(cmd), run.ID); err != nil {
				return err
			}
			if app.Emit(map[string]any{"run": run.ID, "pane": run.PaneID, "focused": true}) {
				return nil
			}
			app.Print("Focused %s", run.PaneID)
			return nil
		},
	}
}

func newRunLogCommand(app *App) *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "log [RUN-ID]",
		Short: "Read recent terminal output from a run's pane",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			run, err := resolveRun(app, args)
			if err != nil {
				return err
			}
			if run.PaneID == "" {
				return Usage("run %s never got a pane", run.ID)
			}
			output, err := app.Herdr().ReadPane(contextFor(cmd), run.PaneID, "recent-unwrapped", lines)
			if err != nil {
				return err
			}
			if app.Emit(map[string]any{"run": run.ID, "pane": run.PaneID, "output": output}) {
				return nil
			}
			app.Print("%s", output)
			return nil
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 120, "how many lines to read")
	return cmd
}

// resolveRun finds the run a command should act on: an explicit id, else
// $HVB_RUN_ID (what a dispatched agent has), else the newest active run.
func resolveRun(app *App, args []string) (*runs.Run, error) {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return app.Store().Get(strings.TrimSpace(args[0]))
	}
	if id := os.Getenv("HVB_RUN_ID"); id != "" {
		return app.Store().Get(id)
	}
	active, err := app.Store().Active()
	if err != nil {
		return nil, err
	}
	switch len(active) {
	case 0:
		return nil, Usage("no run id given, $HVB_RUN_ID is not set, and no run is active")
	case 1:
		return active[0], nil
	default:
		return nil, Usage("no run id given and %d runs are active; name one (`hvb run list`)", len(active))
	}
}
