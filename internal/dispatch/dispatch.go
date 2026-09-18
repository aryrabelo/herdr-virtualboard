package dispatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// Dispatcher launches feature agents into Herdr panes.
type Dispatcher struct {
	Workspace *workspace.Workspace
	Config    *config.Config
	Herdr     *herdrcli.Client
	VB        *vb.Client
	Store     *runs.Store
	Roles     []roles.Role
	// GitBin overrides the git executable, for tests.
	GitBin string
	// SelfRunID is the run whose agent is the caller, from $HVB_RUN_ID.
	// Reconcile never parks it: an agent asking about its own run is
	// definitionally still alive, and parking it would be the board
	// contradicting direct evidence.
	SelfRunID string
}

// Request is one dispatch.
type Request struct {
	Spec *feature.Spec
	// Role overrides the suggested role. Empty means suggest one.
	Role string
	// Kind overrides the harness. Empty means the column's, then the
	// config's default.
	Kind string
	// Focus switches the user's view to the new agent pane.
	Focus bool
	// Force takes the vb lock even when another owner holds it.
	Force bool
	// Worktree runs the agent in an isolated checkout. Nil inherits the
	// column's setting, then the global one — the tri-state matters because
	// "the user did not say" and "the user said no" must resolve
	// differently against a config that defaults it on.
	Worktree *bool
	// PR opens a pull request when the run succeeds. Nil inherits.
	PR *bool
}

// wantsWorktree resolves the tri-state against configuration.
func (d *Dispatcher) wantsWorktree(req Request, column config.Column) bool {
	if req.Worktree != nil {
		return *req.Worktree
	}
	return column.UseWorktree(d.Config.Worktree.Enabled)
}

// wantsPR resolves the tri-state against configuration. A pull request without
// a worktree is meaningless — there would be no branch to open it from — so it
// is only ever true alongside one.
func (d *Dispatcher) wantsPR(req Request, column config.Column, worktree bool) bool {
	if !worktree {
		return false
	}
	if req.PR != nil {
		return *req.PR
	}
	return column.UsePR(d.Config.Forge.Enabled)
}

// ErrLocked reports that another owner holds the feature's vb lock.
var ErrLocked = errors.New("feature is locked by another owner")

// ErrUnknownHarness reports a harness Herdr cannot start. It is caller input,
// so the CLI maps it to a usage exit rather than a board failure.
var ErrUnknownHarness = errors.New("unknown harness")

// Start dispatches a feature and returns its run record.
//
// The launch is pane-first, which is Herdr's documented contract and not an
// implementation detail worth varying: `agent start` never creates layout, so
// the pane must exist, with its cwd and environment already set, before the
// harness is asked to occupy it.
//
// A plain run gets its own workspace, filed under the same sidebar group as
// the workspace hosting the project:
//
//	group `bugtoprompt`             the project workspace's own group
//	 └─ workspace `ftr-0007 · qa`   one per run, cwd = project root
//	     └─ root pane               env = the run's variables; the harness
//
// A worktree run is placed inside the linked workspace Herdr already opened
// for the checkout, where it is a pane in the feature's tab:
//
//	tab   `ftr-0007`        stable per feature, reused across runs
//	 └─ anchor pane         a plain shell; the split parent
//	     └─ run pane        cwd = the checkout, env = the run's variables
//
// After a successful launch the anchor is closed, leaving exactly the harness
// pane visible. A failed launch keeps the anchor: it is the evidence of what
// went wrong, and closing it would hide the error from the user.
func (d *Dispatcher) Start(ctx context.Context, req Request) (*runs.Run, error) {
	if req.Spec == nil {
		return nil, fmt.Errorf("dispatch: no feature given")
	}
	// Input is validated before the gate: a typo in --harness should not
	// need a running Herdr to be reported. The refusal comes first of all,
	// so a card no agent may take costs no pane, no vb lock, no worktree
	// and no run record.
	column := d.Config.Column(req.Spec.Status)
	role, humanOnly, err := d.resolveRole(req, column)
	if err != nil {
		return nil, err
	}
	kind := d.resolveKind(req, column, humanOnly)
	if !herdrcli.ValidKind(kind) {
		return nil, fmt.Errorf("%w: %q is not a harness herdr can start (see `hvb harness list`)", ErrUnknownHarness, kind)
	}
	if err := d.Herdr.Gate(ctx); err != nil {
		return nil, err
	}

	if err := d.claim(ctx, req); err != nil {
		return nil, err
	}

	useWorktree := d.wantsWorktree(req, column)
	wantPR := d.wantsPR(req, column, useWorktree)

	run := &runs.Run{
		ID:        runs.NewID(req.Spec.ID),
		FeatureID: req.Spec.ID,
		Title:     req.Spec.Title,
		Status:    req.Spec.Status,
		Role:      role.Key,
		Kind:      kind,
		State:     runs.Queued,
		StartedAt: time.Now().UTC(),
	}
	if wantPR {
		// Recorded at dispatch so a completion knows a pull request was
		// asked for even if the configuration changed in between.
		run.PullRequest = &runs.PullRequest{}
	}

	// The worktree is cut before the run is stored: a failure here means no
	// dispatch happened at all, and leaving a queued run behind for a branch
	// that was never created would be a lie.
	if useWorktree {
		worktree, err := d.prepareWorktree(ctx, req.Spec)
		if err != nil {
			d.releaseLock(ctx, req.Spec.ID)
			return nil, err
		}
		run.Worktree = worktree
	}

	// A missing charter degrades the prompt but must not abort the dispatch.
	// It is reported below, once a run record exists to hold the note.
	charter, charterErr := role.Charter()
	if charterErr != nil {
		charter = ""
		if role.Path == "" {
			// No charter file was ever loaded, which is roles.Load's
			// documented fallback and not a fault worth reporting.
			charterErr = nil
		}
	}
	// The prompt file is written before the run is stored, for the same
	// reason the worktree is cut before it: a failure here means no dispatch
	// happened at all. This one refuses rather than degrades — an agent
	// started without its task is an agent with a shell in the owner's
	// repository and no instructions, which is worse than one never started.
	promptPath, err := d.writePrompt(req, run, role, column, charter)
	if err != nil {
		d.releaseLock(ctx, req.Spec.ID)
		return nil, err
	}

	if err := d.Store.Append(run); err != nil {
		return nil, err
	}
	if charterErr != nil {
		d.note(run.ID, fmt.Sprintf("role charter unreadable: %v", charterErr))
	}

	if err := d.launch(ctx, req, run, role, column, kind, promptPath); err != nil {
		if _, finishErr := d.Store.Finish(run.ID, runs.Failed, "dispatch_error", err.Error()); finishErr != nil {
			return nil, errors.Join(err, finishErr)
		}
		d.releaseLock(ctx, req.Spec.ID)
		return nil, err
	}
	return d.Store.Get(run.ID)
}

func (d *Dispatcher) launch(ctx context.Context, req Request, run *runs.Run, role roles.Role, column config.Column, kind, promptPath string) error {
	cwd := d.Workspace.Root
	if run.Worktree != nil {
		cwd = run.Worktree.Path
	}

	env := Env(req.Spec, run.ID, role.Key, d.Workspace.Root, column)
	if run.Worktree != nil {
		// The agent works in the checkout, but the board it reports to is
		// still the one in the main repository, so the two are separate.
		env["HVB_WORKTREE"] = run.Worktree.Path
		env["HVB_BRANCH"] = run.Worktree.Branch
		env["HVB_BASE_BRANCH"] = run.Worktree.Base
	}

	var workspaceID, tabID, anchorID, paneID string
	if run.Worktree != nil {
		// A worktree run already has its workspace: the linked one Herdr
		// opened for the checkout. The run belongs inside it, because that
		// workspace *is* the checkout's place in the sidebar, so here the
		// run is a pane in the feature's tab.
		workspaceID = run.Worktree.WorkspaceID
		var err error
		if tabID, anchorID, err = d.resolveTab(ctx, workspaceID, run, env, cwd); err != nil {
			return err
		}
		// Recorded before the split so a failed split still leaves behind
		// the tab and anchor it was going to happen in.
		if _, err := d.Store.Update(run.ID, func(r *runs.Run) error {
			r.WorkspaceID, r.TabID, r.AnchorPane = workspaceID, tabID, anchorID
			return nil
		}); err != nil {
			return err
		}
		pane, err := d.Herdr.SplitPane(ctx, anchorID, "right", cwd, env, req.Focus)
		if err != nil {
			return fmt.Errorf("split run pane: %w", err)
		}
		paneID = pane.PaneID
	} else {
		workspace, tab, root, err := d.createRunWorkspace(ctx, run, role, env, cwd, req.Focus)
		if err != nil {
			return err
		}
		workspaceID, tabID, paneID = workspace.WorkspaceID, tab.TabID, root.PaneID
		if _, err := d.Store.Update(run.ID, func(r *runs.Run) error {
			r.WorkspaceID, r.TabID = workspaceID, tabID
			return nil
		}); err != nil {
			return err
		}
	}

	agentName := runs.AgentName(run.FeatureID, run.ID)
	if _, err := d.Store.Update(run.ID, func(r *runs.Run) error {
		r.PaneID, r.AgentName, r.State = paneID, agentName, runs.Running
		return nil
	}); err != nil {
		return err
	}
	// Best-effort cosmetics: a pane that could not be relabelled still runs.
	_ = d.Herdr.RenamePane(ctx, paneID, runs.PaneLabel(run.FeatureID, role.Key))

	blocked, err := d.startAgent(ctx, agentName, kind, paneID)
	if err != nil {
		return fmt.Errorf("start %s agent: %w", kind, err)
	}

	// The anchor has done its job. Closing a split parent is safe — the
	// child keeps its process and environment — and leaves the user looking
	// at one pane per run instead of two. A run in its own workspace never
	// had one.
	if anchorID != "" {
		if err := d.Herdr.ClosePane(ctx, anchorID); err == nil {
			if _, updateErr := d.Store.Update(run.ID, func(r *runs.Run) error {
				r.AnchorPane = ""
				return nil
			}); updateErr != nil {
				return updateErr
			}
		}
	}

	return d.submitPrompt(ctx, run.ID, agentName, FilePointerPrompt(promptPath), blocked)
}

// writePrompt renders the run's prompt to a file and returns its path.
//
// The prompt is delivered by reference, never by value. `bora agent prompt`
// takes the text in argv and the host hands it to the harness wrapped in a
// bracketed paste, which the harness is free to collapse — measured on the
// owner's session, an 11 KB prompt arrived as `[Paste #1, +222 lines]` and the
// body never reached the model: the agent went looking for the content in
// `local://` and in its session directory, found nothing, and stopped. The
// pane input queue is capped as well, so argv delivery of a prompt this size
// is fragile for two independent reasons. A path is short enough for both, and
// a file is a thing every harness can read.
//
// The file lives beside the run store, which is where hvb's other
// machine-local bookkeeping about a run already lives: same lifetime, same
// directory, inspectable long after the pane is gone. It deliberately does not
// live in the repository — a file written there would turn up in the agent's
// own `git status` on its first turn, and in its commit if it is careless.
func (d *Dispatcher) writePrompt(req Request, run *runs.Run, role roles.Role, column config.Column, charter string) (string, error) {
	path, err := WritePromptFile(promptsDir(d.Store), PromptInput{
		Spec:     req.Spec,
		Role:     role,
		Charter:  charter,
		Column:   column,
		RunID:    run.ID,
		RelPath:  d.Workspace.Rel(req.Spec.Path),
		Worktree: run.Worktree,
		WantPR:   run.PullRequest != nil,
	})
	if err != nil {
		return "", fmt.Errorf("write the run's prompt file: %w", err)
	}
	return path, nil
}

// promptsDir is the directory holding run prompt files, a sibling of the run
// file itself. WritePromptFile creates it.
func promptsDir(store *runs.Store) string {
	return filepath.Join(filepath.Dir(store.Path()), "prompts")
}

// createRunWorkspace opens the run its own Herdr workspace, filed under the
// same sidebar group as the workspace hosting the project.
//
// A workspace, and not a split off the caller's pane. A split puts the harness
// wherever the board happens to be running: a board in some other workspace's
// tab dispatches an agent into that tab, which is exactly where the owner
// found one. A run is its own unit of work and gets its own row in the
// sidebar, next to the repository it works on.
func (d *Dispatcher) createRunWorkspace(ctx context.Context, run *runs.Run, role roles.Role, env map[string]string, cwd string, focus bool) (*herdrcli.Workspace, *herdrcli.Tab, *herdrcli.Pane, error) {
	group, err := d.sidebarGroup(ctx)
	if err != nil {
		// Grouping is cosmetic, so its failures are notes. An agent
		// working in an ungrouped workspace is a workspace in the wrong
		// row; a refused dispatch is work that did not happen.
		d.note(run.ID, fmt.Sprintf("could not read the project's sidebar group: %v", err))
	}
	// The same label the run's pane carries, from the same function: two
	// spellings of one name drift, and the owner reads both in one sidebar.
	label := runs.PaneLabel(run.FeatureID, role.Key)
	workspace, tab, root, err := d.Herdr.CreateWorkspace(ctx, cwd, label, env, focus)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create run workspace: %w", err)
	}
	if group != "" {
		if err := d.Herdr.SetWorkspaceGroup(ctx, workspace.WorkspaceID, group); err != nil {
			d.note(run.ID, fmt.Sprintf("workspace %s could not join the %q group: %v", workspace.WorkspaceID, group, err))
		}
	}
	return workspace, tab, root, nil
}

// sidebarGroup is the group a run's workspace joins: the one already holding
// the workspace whose panes run in the project root.
//
// Derived, never a constant. The board runs over whatever repository it was
// opened in, and the grouping is the owner's own — on his session
// `bugtoprompt-api` sits in a group called `bugtoprompt`, and nothing in hvb
// may know that name.
//
// A project root with no workspace, or one whose workspace is in no group,
// degrades to no group at all: the run's workspace is created at the top
// level, where it is still visible and still works. Inventing a name instead
// would add a sidebar folder the owner never made, which is a worse outcome
// than a row in the wrong place.
func (d *Dispatcher) sidebarGroup(ctx context.Context) (string, error) {
	hostID, found, err := d.Herdr.WorkspaceForPath(ctx, d.Workspace.Root)
	if err != nil {
		return "", fmt.Errorf("find the project's workspace: %w", err)
	}
	if !found {
		return "", nil
	}
	workspaces, err := d.Herdr.Workspaces(ctx)
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == hostID {
			return workspace.VisualGroup, nil
		}
	}
	return "", nil
}

// submitPrompt sends the task, deferring it while the harness is blocked.
//
// hvb never answers a startup dialog itself. Whether to trust a directory is
// the user's decision, and a board that clicks "yes" on their behalf would be
// making a security choice nobody asked it to make. Instead the prompt is
// parked on the run and Reconcile submits it once the agent settles.
//
// prompt is the short pointer at the run's prompt file, not the task text, so
// run.PendingPrompt parks the pointer too. That is the point: Reconcile's
// deferred submission (complete.go, submitPending) re-sends this exact string
// through `agent prompt`, and parking the 11 KB body would put the collapsed
// paste back on the one path that is hardest to notice — nobody is watching
// when it fires. Every reader of the field only asks whether it is empty
// (cli/run.go, tui/detail.go), so they are unaffected by which string it is.
func (d *Dispatcher) submitPrompt(ctx context.Context, runID, agentName, prompt string, blocked bool) error {
	if !blocked {
		if err := d.Herdr.PromptAgent(ctx, agentName, prompt, false, 0); err != nil {
			if !herdrcli.IsAgentBlocked(err) {
				return fmt.Errorf("submit prompt: %w", err)
			}
			blocked = true
		}
	}
	if !blocked {
		return nil
	}
	_, err := d.Store.Update(runID, func(r *runs.Run) error {
		r.PendingPrompt = prompt
		return nil
	})
	if err != nil {
		return err
	}
	d.note(runID, "the harness is waiting on its own startup dialog — answer it in the pane "+
		"(`hvb run focus`) and hvb will submit the task automatically")
	return nil
}

// resolveTab reuses the feature's existing tab when one is open, so repeated
// runs on a feature stay in one place. It returns the tab and an anchor pane
// inside it that is safe to split from.
//
// Only the worktree path calls this: a plain run has a workspace of its own
// and its harness occupies that workspace's root pane, so there is no tab to
// reuse and nothing to split from.
func (d *Dispatcher) resolveTab(ctx context.Context, workspaceID string, run *runs.Run, env map[string]string, cwd string) (tabID, anchorID string, err error) {
	label := runs.TabLabel(run.FeatureID)
	tabs, err := d.Herdr.Tabs(ctx, workspaceID)
	if err != nil {
		return "", "", fmt.Errorf("list tabs: %w", err)
	}
	for _, tab := range tabs {
		if tab.Label != label {
			continue
		}
		anchor, err := d.anchorInTab(ctx, tab.TabID, run, env, cwd)
		if err != nil {
			return "", "", err
		}
		return tab.TabID, anchor, nil
	}

	tab, root, err := d.Herdr.CreateTab(ctx, workspaceID, cwd, label, env, false)
	if err != nil {
		return "", "", fmt.Errorf("create tab: %w", err)
	}
	_ = d.Herdr.RenamePane(ctx, root.PaneID, runs.AnchorLabel(run.FeatureID))
	return tab.TabID, root.PaneID, nil
}

// anchorInTab picks a pane in an existing feature tab to split from. A pane
// hosting a live agent is never chosen: `agent start` requires an available
// shell, and splitting from a busy harness pane is how two runs end up fighting
// over one terminal.
//
// The split at the end of this function stays. It is not the misplacement the
// workspace path fixed: it never decides which workspace a run lands in — the
// worktree's linked workspace already did that — and it only fires when every
// pane in the feature's own tab is occupied, which is the second run of a
// feature inside its own checkout. What it produces is a plain shell to launch
// from, in the tab the run already belongs to.
func (d *Dispatcher) anchorInTab(ctx context.Context, tabID string, run *runs.Run, env map[string]string, cwd string) (string, error) {
	panes, err := d.Herdr.Panes(ctx, "")
	if err != nil {
		return "", fmt.Errorf("list panes: %w", err)
	}
	agents, err := d.Herdr.Agents(ctx)
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	busy := map[string]bool{}
	for _, agent := range agents {
		busy[agent.PaneID] = true
	}
	for _, pane := range panes {
		if pane.TabID == tabID && !busy[pane.PaneID] {
			return pane.PaneID, nil
		}
	}
	// Every pane in the tab is occupied: split a fresh anchor off the first
	// one, which Herdr allows even when that pane runs an agent, because the
	// new child is a plain shell.
	for _, pane := range panes {
		if pane.TabID != tabID {
			continue
		}
		anchor, err := d.Herdr.SplitPane(ctx, pane.PaneID, "down", cwd, env, false)
		if err != nil {
			return "", fmt.Errorf("create anchor pane: %w", err)
		}
		_ = d.Herdr.RenamePane(ctx, anchor.PaneID, runs.AnchorLabel(run.FeatureID))
		return anchor.PaneID, nil
	}
	return "", fmt.Errorf("tab %s has no panes to split from", tabID)
}

// claim takes the vb lock and records the owner on the feature, so a second
// board, a second agent, or a human running `vb` sees the feature is taken.
func (d *Dispatcher) claim(ctx context.Context, req Request) error {
	if d.Config.LockTTLMinutes <= 0 {
		return nil
	}
	owner := d.Config.ResolveOwner()
	_, err := d.VB.Acquire(ctx, req.Spec.ID, owner, d.Config.LockTTLMinutes, req.Force)
	if err == nil {
		return nil
	}
	var vbErr *vb.Error
	if errors.As(err, &vbErr) && vbErr.Code == vb.ExitLockConflict {
		return fmt.Errorf("%w: %s (retry with --force to take it)", ErrLocked, vbErr.Message)
	}
	return err
}

func (d *Dispatcher) releaseLock(ctx context.Context, featureID string) {
	if d.Config.LockTTLMinutes <= 0 {
		return
	}
	_ = d.VB.Release(ctx, featureID)
}

// resolveRole picks the charter this dispatch runs under. The second result
// reports a human-only card: one the owner's own hands have to close, routed
// here to the single charter written for exactly that.
//
// The routing is consulted before req.Role and column.Role, which is a
// deliberate change of precedence and not an accident of ordering.
// roles.Suggest already decided that `hitl` beats the card's own `role:`
// label; a flag that outranked it here would make the same card land on an
// implementer depending on which layer answered, and that incoherence is
// worse than either rule alone. It is not hypothetical: config.Default gives
// the review column role = "qa", so a branch placed after the explicit-role
// one would be bypassed by stock configuration for every `hitl` card that
// reaches review. The owner who means an implementer anyway drops the label,
// which is the same act as saying the human half is done.
//
// roles.ErrNoCharter is not a refusal and keeps its fallback: a workspace with
// no agents directory still dispatches under the configured role, which is
// what roles.Load's doc promises.
func (d *Dispatcher) resolveRole(req Request, column config.Column) (roles.Role, bool, error) {
	suggested, err := roles.Suggest(d.Roles, req.Spec, d.Config.Role)
	if errors.Is(err, roles.ErrHumanOnly) {
		role, routeErr := d.unblocker(err)
		return role, true, routeErr
	}
	for _, want := range []string{req.Role, column.Role} {
		if want == "" {
			continue
		}
		if role, found := roles.Find(d.Roles, want); found {
			return role, false, nil
		}
	}
	if err != nil {
		return roles.Role{Key: d.Config.Role, Name: d.Config.Role}, false, nil
	}
	return suggested, false, nil
}

// unblocker resolves the charter a human-only card is routed to, or repeats
// the refusal when the workspace does not ship it.
//
// The missing charter degrades to a refusal and never to a default role. A
// fallback here would re-create, by a new door, the exact defect this path
// exists to fix: an implementer launched at a card whose whole content is
// something only the owner can do. So the failure says which key was looked
// for and where, which is a fixable sentence, and an operator who sets
// HumanOnlyRole to nothing has turned the routing off and gets the plain
// refusal back.
func (d *Dispatcher) unblocker(refusal error) (roles.Role, error) {
	key := strings.TrimSpace(d.Config.HumanOnlyRole)
	if key == "" {
		return roles.Role{}, refusal
	}
	if role, found := roles.Find(d.Roles, key); found {
		return role, nil
	}
	return roles.Role{}, fmt.Errorf("%w; it routes to the %q charter, which this workspace does not ship — write %s in %s",
		refusal, key, key+".md", d.chartersDir())
}

// chartersDir is the directory the loaded charters came from, which is not
// always the workspace's own: `hvb queue --charters <dir>` points a board at a
// vault's agents directory while it works in a different repository
// (cli/queue.go:276-292). Deriving it from a charter that did load keeps the
// refusal's "write it here" pointing at the directory hvb actually reads,
// instead of one it would only read in the other configuration.
func (d *Dispatcher) chartersDir() string {
	for _, role := range d.Roles {
		if role.Path != "" {
			return filepath.Dir(role.Path)
		}
	}
	return d.Workspace.AgentsDir()
}

// resolveKind picks the harness. humanOnly comes from resolveRole and moves
// the configured unblocker harness in ahead of the column and the global
// default, because both of those describe ordinary work: the Blocked column a
// `hitl` card lands in (fila.ColumnFor) also holds every card the frontier
// blocked, and config.Default already proves stock configuration injects
// per-column values with nobody typing a flag — it gives the review column
// role = "qa". A per-column harness would therefore decide the harness for a
// card that is only passing through that column.
//
// req.Kind still wins. It is a human typing --harness at this dispatch, the
// most explicit statement there is about which program runs, and silently
// ignoring it would make the flag a lie. Nothing about the invariant depends
// on it: the charter is what must never be an implementer's, and that is
// decided in resolveRole, where no flag gets past.
func (d *Dispatcher) resolveKind(req Request, column config.Column, humanOnly bool) string {
	candidates := []string{req.Kind, column.Harness, d.Config.Harness}
	if humanOnly {
		candidates = []string{req.Kind, d.Config.HumanOnlyHarness, column.Harness, d.Config.Harness}
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return "claude"
}

// startAgent starts the harness, waiting out the moment between a pane being
// created and its shell reaching an interactive prompt.
//
// Herdr refuses `agent start` on a pane that is not an available shell, and a
// pane split microseconds ago is never one yet. Because hvb created this pane
// itself and nothing else can be using it, retrying is safe here in a way it
// would not be for a pane the user is typing in.
//
// The second result reports that the agent started but is blocked on its own
// startup UI. That is not a failure: the agent is live, its name is valid, and
// a human answering the dialog is all that stands between it and working. A
// fresh worktree is a directory the harness has never seen, so on that path it
// is the ordinary case.
func (d *Dispatcher) startAgent(ctx context.Context, name, kind, paneID string) (blocked bool, err error) {
	deadline := time.Now().Add(paneReadyWait)
	for {
		err = d.Herdr.StartAgent(ctx, name, kind, paneID, d.Config.StartTimeout.Duration(), nil)
		switch {
		case err == nil:
			return false, nil
		case herdrcli.IsAgentNotReady(err):
			return true, nil
		case !herdrcli.IsPaneBusy(err):
			return false, err
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("%w (the pane never reached an interactive prompt within %s)", err, paneReadyWait)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(paneReadyPoll):
		}
	}
}

// How long to wait for a freshly split pane to reach its prompt. A shell that
// has not printed a prompt within this is not slow, it is broken or is
// something other than a shell.
const (
	paneReadyWait = 10 * time.Second
	paneReadyPoll = 250 * time.Millisecond
)

func (d *Dispatcher) note(runID, message string) {
	_, _ = d.Store.Update(runID, func(r *runs.Run) error {
		r.LastErrors = append(r.LastErrors, message)
		return nil
	})
}
