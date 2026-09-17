package cli

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/fios"
	"github.com/virtualboard/herdr-virtualboard/internal/ghboard"
	"github.com/virtualboard/herdr-virtualboard/internal/tui"
)

// defaultQueueWindow is the week the pull-request column covers.
const defaultQueueWindow = 7 * 24 * time.Hour

// newQueueCommand opens the same board over the owner's real queue instead of
// VirtualBoard spec markdown.
//
// It is a sibling of `hvb tui`, not a flag on it: the two read different worlds
// and only one of them can write. Keeping them apart means `hvb tui` resolves a
// workspace, checks for vb, and dispatches exactly as it always did, while
// `hvb queue` needs neither a .virtualboard directory nor the vb binary.
func newQueueCommand(app *App) *cobra.Command {
	var (
		vault   string
		repo    string
		window  time.Duration
		refresh time.Duration
		colour  bool
	)
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Open the board over FIOS.md, gates/ and this week's pull requests",
		Long: `Open the kanban board over the queue that already exists, rather than over
VirtualBoard spec markdown.

--vault reads the owner's own queue: FIOS.md and the gate ledgers in gates/.
--repo reads the pull requests of one GitHub repository through the gh CLI,
each card carrying the issue it closes.

Every source is read-only. Moving, editing or dispatching a card is refused
with the file that owns it, because FIOS.md and gates/ have their own parser
and their own convention; the board never writes to them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := contextFor(cmd)

			vault = strings.TrimSpace(vault)
			repo = strings.TrimSpace(repo)
			if vault == "" && repo == "" {
				return Usage("nothing to read: pass --vault with the directory holding FIOS.md, --repo with an owner/name, or both")
			}

			var sources []tui.Source
			var names []string
			if vault != "" {
				resolved, err := filepath.Abs(vault)
				if err != nil {
					return err
				}
				if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
					return Usage("--vault %s is not a directory holding FIOS.md", vault)
				}
				sources = append(sources, fios.New(resolved))
				names = append(names, filepath.Base(resolved))
			}
			if repo != "" {
				if strings.Count(repo, "/") != 1 {
					return Usage("--repo %s is not in owner/name form", repo)
				}
				sources = append(sources, ghboard.New(repo, window))
				names = append(names, repo)
			}

			// The global configuration still applies: it carries the owner
			// handle and the colour the user already chose. A vault has no
			// project config, and a missing one is normal.
			cfg, err := config.Load(vault)
			if err != nil {
				return err
			}

			backend := tui.NewSourceBackend(strings.Join(names, " + "), cfg, cfg.ResolveOwner(), sources...)

			// SIGTERM must reach the loop as a cancellation rather than kill
			// the process: the terminal has to be restored first.
			ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
			defer stop()

			return tui.Run(ctx, backend, tui.Options{
				Refresh:   refresh,
				Colour:    colour,
				PaneTitle: app.paneTitle(ctx),
			})
		},
	}
	cmd.Flags().StringVar(&vault, "vault", os.Getenv("HVB_QUEUE_VAULT"), "directory holding FIOS.md and gates/ (default: $HVB_QUEUE_VAULT)")
	cmd.Flags().StringVar(&repo, "repo", os.Getenv("HVB_QUEUE_REPO"), "GitHub repository as owner/name, read through gh (default: $HVB_QUEUE_REPO)")
	cmd.Flags().DurationVar(&window, "window", defaultQueueWindow, "how far back pull requests count as this week's")
	cmd.Flags().DurationVar(&refresh, "refresh", tui.DefaultRefresh, "how often to reload the board")
	cmd.Flags().BoolVar(&colour, "color", true, "use colour (NO_COLOR also disables it)")
	return cmd
}
