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
	"github.com/virtualboard/herdr-virtualboard/internal/issuesrc"
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
		vault        string
		repo         string
		issues       string
		label        string
		kitPath      string
		project      string
		frontierRepo string
		limit        int
		window       time.Duration
		refresh      time.Duration
		colour       bool
	)
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Open the board over FIOS.md, gates/ and this week's pull requests",
		Long: `Open the kanban board over the queue that already exists, rather than over
VirtualBoard spec markdown.

--vault reads the owner's own queue: FIOS.md and the gate ledgers in gates/.
--repo reads the pull requests of one GitHub repository through the gh CLI,
each card carrying the issue it closes.
--issues reads the issue queue of a CEO repository: usina routes it and ranks
it, kit.py's frontier says what is ready and what is blocked, and gh answers
when usina cannot. Whichever source answered is named on every card and every
degradation is reported on the board instead of being silently absorbed.

Every source is read-only. Moving, editing or dispatching a card is refused
with the place that owns it, because FIOS.md, gates/ and the issue queue each
have their own parser and their own convention; the board never writes to them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := contextFor(cmd)

			vault = strings.TrimSpace(vault)
			repo = strings.TrimSpace(repo)
			issues = strings.TrimSpace(issues)
			if vault == "" && repo == "" && issues == "" {
				return Usage("nothing to read: pass --vault with the directory holding FIOS.md, --repo with an owner/name, --issues with a CEO repository, or any combination")
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
			if issues != "" {
				if strings.Count(issues, "/") != 1 {
					return Usage("--issues %s is not in owner/name form", issues)
				}
				// The frontier is optional as a set: kit.py ranks one
				// project at a time, so asking for it needs the script,
				// the project slug, AND the repository its issue numbers
				// belong to. That last one is not bureaucracy — measured
				// 2026-09-17, `kit.py fronteira bugtoprompt` ranks
				// aryrabelo/bugtoprompt#31 while aryrabelo/ceo-bora#31 is
				// an unrelated closed issue, and kit.py's answer names no
				// repository at all. Without it the join would rank, or
				// block, whichever card shares the integer.
				kit, slug := strings.TrimSpace(kitPath), strings.TrimSpace(project)
				kitRepo := strings.TrimSpace(frontierRepo)
				asked := kit != "" || slug != "" || kitRepo != ""
				switch {
				case asked && kit == "":
					return Usage("the frontier needs --kit with the path to kit.py")
				case asked && slug == "":
					return Usage("the frontier needs --project with the slug kit.py ranks")
				case asked && kitRepo == "":
					return Usage("the frontier needs --kit-repo with the owner/name whose issue numbers kit.py ranks (kit.py does not say)")
				case kitRepo != "" && strings.Count(kitRepo, "/") != 1:
					return Usage("--kit-repo %s is not in owner/name form", kitRepo)
				}
				// usina and kit.py both resolve WHICH queue they are
				// talking about from the working directory, so the
				// children run in the CEO repository rather than in
				// whatever worktree the board was launched from. The
				// repository is kit.py's own grandparent (bin/kit.py);
				// without --kit there is nothing to derive it from and
				// the caller has to already be there.
				from := ""
				if kit != "" {
					from = filepath.Dir(filepath.Dir(kit))
				}
				sources = append(sources, issuesrc.New(
					issuesrc.DirRunner(from), issues, label, kit, slug, kitRepo, limit))
				names = append(names, issues+" issues")
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
	cmd.Flags().StringVar(&issues, "issues", os.Getenv("HVB_QUEUE_ISSUES"), "CEO repository as owner/name whose issue queue to read through usina (default: $HVB_QUEUE_ISSUES)")
	cmd.Flags().StringVar(&label, "label", os.Getenv("HVB_QUEUE_LABEL"), "only issues carrying this label, e.g. project:bugtoprompt (default: $HVB_QUEUE_LABEL)")
	cmd.Flags().StringVar(&kitPath, "kit", os.Getenv("HVB_QUEUE_KIT"), "path to kit.py, whose frontier ranks the issues (default: $HVB_QUEUE_KIT)")
	cmd.Flags().StringVar(&frontierRepo, "kit-repo", os.Getenv("HVB_QUEUE_KIT_REPO"), "owner/name whose issue numbers kit.py's frontier ranks; required with --kit because kit.py does not say (default: $HVB_QUEUE_KIT_REPO)")
	cmd.Flags().StringVar(&project, "project", os.Getenv("HVB_QUEUE_PROJECT"), "project slug kit.py ranks, e.g. bugtoprompt (default: $HVB_QUEUE_PROJECT)")
	cmd.Flags().IntVar(&limit, "limit", issuesrc.DefaultLimit, "how many issues to read at most")
	cmd.Flags().DurationVar(&window, "window", defaultQueueWindow, "how far back pull requests count as this week's")
	cmd.Flags().DurationVar(&refresh, "refresh", tui.DefaultRefresh, "how often to reload the board")
	cmd.Flags().BoolVar(&colour, "color", true, "use colour (NO_COLOR also disables it)")
	return cmd
}
