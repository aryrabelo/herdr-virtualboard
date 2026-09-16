package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/netors/herdr-virtualboard/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICommand(app *App) *cobra.Command {
	var (
		refresh time.Duration
		colour  bool
	)
	cmd := &cobra.Command{
		Use:     "tui",
		Aliases: []string{"board"},
		Short:   "Open the kanban board",
		Long: `Open the kanban board over this VirtualBoard workspace.

Columns are the VirtualBoard lifecycle: backlog, in-progress, blocked, review,
done. Cards are feature specs. Moving a card runs vb, so the repository and the
board can never disagree; pressing d dispatches a role agent into a Herdr pane.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Resolve(); err != nil {
				return err
			}
			if err := app.VB().Available(); err != nil {
				return err
			}

			backend := tui.NewBackend(
				app.Workspace(), app.Config(), app.VB(), app.Herdr(),
				app.Store(), app.Dispatcher(), app.Roles(),
			)

			// SIGINT and SIGTERM must reach the loop as a cancellation, not
			// kill the process: the terminal has to be restored first.
			ctx, stop := signal.NotifyContext(contextFor(cmd), syscall.SIGTERM)
			defer stop()

			return tui.Run(ctx, backend, tui.Options{
				Refresh:   refresh,
				Colour:    colour,
				PaneTitle: app.paneTitle(ctx),
			})
		},
	}
	cmd.Flags().DurationVar(&refresh, "refresh", tui.DefaultRefresh, "how often to reload the board")
	cmd.Flags().BoolVar(&colour, "color", true, "use colour (NO_COLOR also disables it)")
	return cmd
}

// paneTitle returns a callback that relabels the plugin's own Herdr pane, or
// nil when hvb is not running inside one.
func (a *App) paneTitle(ctx context.Context) func(string) {
	paneID := os.Getenv("HERDR_PANE_ID")
	if paneID == "" {
		return nil
	}
	return func(title string) {
		// Cosmetic: a failed rename leaves the default border and is not
		// worth interrupting the board for.
		_ = a.Herdr().RenamePane(ctx, paneID, title)
	}
}
