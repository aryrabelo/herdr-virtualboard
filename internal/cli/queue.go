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
	"github.com/virtualboard/herdr-virtualboard/internal/dispatch"
	"github.com/virtualboard/herdr-virtualboard/internal/fios"
	"github.com/virtualboard/herdr-virtualboard/internal/ghboard"
	"github.com/virtualboard/herdr-virtualboard/internal/issuesrc"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/tui"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// queueProjectID is the run-store key for a queue board, derived from the
// directory its agents work in.
//
// The prefix is what keeps it out of vb's namespace. A run store is one file
// named after this id (runs/<id>.json), and a VirtualBoard project's id is a
// bare 16 hex characters (workspace.go:92-95) — so pointing --work-root at a
// directory that also happens to be a vb workspace gives `queue-<hash>`, a
// separate history, instead of writing the queue's PR- and issue-keyed runs
// into the runs of a board whose features they are not.
func queueProjectID(ws *workspace.Workspace) string { return "queue-" + ws.ID() }

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
		charters     string
		workRoot     string
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

Every source is read-only. Moving, editing or creating a card is refused with
the place that owns it, because FIOS.md, gates/ and the issue queue each have
their own parser and their own convention; the board never writes to them.

Dispatching is the exception, and it is opt-in as a pair: --charters with the
directory holding the role charters, and --work-root with the repository the
agent should work in. With both, pressing d picks a role and launches an agent
through Herdr, as it does on a VirtualBoard board; with neither, d says why it
cannot.
With --vault the charters are derived from <vault>/.virtualboard/agents when
that directory exists.`,
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
			// vaultRoot is remembered because the charters can be derived
			// from it, and the absolute form is the one to derive from.
			vaultRoot := ""
			if vault != "" {
				resolved, err := filepath.Abs(vault)
				if err != nil {
					return err
				}
				if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
					return Usage("--vault %s is not a directory holding FIOS.md", vault)
				}
				vaultRoot = resolved
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

			dispatcher, err := queueDispatch(app, cfg, charters, workRoot, vaultRoot)
			if err != nil {
				return err
			}

			backend := tui.NewSourceBackendWithDispatch(
				strings.Join(names, " + "), cfg, cfg.ResolveOwner(), dispatcher, sources...)

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
	cmd.Flags().StringVar(&charters, "charters", os.Getenv("HVB_QUEUE_CHARTERS"), "directory holding the agent role charters, e.g. <vault>/.virtualboard/agents; requires --work-root (default: $HVB_QUEUE_CHARTERS)")
	cmd.Flags().StringVar(&workRoot, "work-root", os.Getenv("HVB_QUEUE_WORK_ROOT"), "repository a dispatched agent works in; requires --charters (default: $HVB_QUEUE_WORK_ROOT)")
	cmd.Flags().IntVar(&limit, "limit", issuesrc.DefaultLimit, "how many issues to read at most")
	cmd.Flags().DurationVar(&window, "window", defaultQueueWindow, "how far back pull requests count as this week's")
	cmd.Flags().DurationVar(&refresh, "refresh", tui.DefaultRefresh, "how often to reload the board")
	cmd.Flags().BoolVar(&colour, "color", true, "use colour (NO_COLOR also disables it)")
	return cmd
}

// queueDispatch resolves the dispatch half of a queue board: the charters an
// agent can adopt and the repository it works in. A nil dispatcher is not a
// failure — it is the read-only board `hvb queue` has always been.
//
// The two flags are one setting. A charter with no repository has nowhere to
// run, and a repository with no charters leaves the role picker with nothing to
// offer, so asking for either alone is a usage error naming the missing half
// rather than a board where d half-works.
func queueDispatch(app *App, cfg *config.Config, charters, workRoot, vaultRoot string) (*dispatch.Dispatcher, error) {
	charters, workRoot = strings.TrimSpace(charters), strings.TrimSpace(workRoot)
	if charters == "" && workRoot == "" {
		return nil, nil
	}
	if workRoot == "" {
		return nil, Usage("--charters %s needs --work-root with the repository the agent should work in", charters)
	}
	if charters == "" {
		// A vault that keeps its own charters is the ordinary case here:
		// the directory this board reads FIOS.md from is a VirtualBoard
		// workspace too. Deriving saves the flag, and a vault without that
		// directory is not an error — it just cannot answer this question.
		if vaultRoot != "" {
			candidate := filepath.Join(vaultRoot, workspace.Dir, "agents")
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				charters = candidate
			}
		}
	}
	if charters == "" {
		// Two different facts, and pointing at a <vault> placeholder for
		// both is how a message sends someone looking for a path they
		// never gave.
		if vaultRoot == "" {
			return nil, Usage("--work-root %s needs --charters with the directory holding the role charters (or a --vault that has %s)",
				workRoot, filepath.Join(workspace.Dir, "agents"))
		}
		return nil, Usage("--work-root %s needs --charters: %s does not exist",
			workRoot, filepath.Join(vaultRoot, workspace.Dir, "agents"))
	}
	if info, err := os.Stat(charters); err != nil || !info.IsDir() {
		return nil, Usage("--charters %s is not a directory holding role charters", charters)
	}
	loaded, err := roles.Load(charters)
	if err != nil {
		return nil, err
	}
	if len(loaded) == 0 {
		// roles.Load answers zero roles and no error for a directory with
		// nothing in it (roles.go:38-46). That is right for `hvb tui`,
		// where dispatch falls back to a role-less prompt, and wrong here:
		// an empty Roles() is how the board decides it cannot dispatch, so
		// it would go on blaming flags the user did pass.
		return nil, Usage("--charters %s holds no role charters (AGENTS.md and RULES.md do not count as roles)", charters)
	}

	root, err := filepath.Abs(workRoot)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, Usage("--work-root %s is not a directory", workRoot)
	}
	// Symlinks are resolved so two paths to the same repository keep one run
	// history, exactly as workspace.Open does it (workspace.go:74-78).
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	// Hand-built rather than resolved: the agent works in a repository,
	// which need not be a VirtualBoard workspace, and workspace.Open refuses
	// a root without .virtualboard (workspace.go:80-83). Root is what this
	// path actually uses — the agent's cwd, and the git repository a worktree
	// would be cut from. Board is spelled the way vb spells it so nothing
	// downstream has to invent a path.
	ws := &workspace.Workspace{Root: root, Board: filepath.Join(root, workspace.Dir)}
	store, err := runs.Open(queueProjectID(ws))
	if err != nil {
		return nil, err
	}
	return &dispatch.Dispatcher{
		Workspace: ws,
		Config:    cfg,
		Herdr:     app.Herdr(),
		VB:        vb.New(root),
		Store:     store,
		Roles:     loaded,
		SelfRunID: os.Getenv("HVB_RUN_ID"),
	}, nil
}
