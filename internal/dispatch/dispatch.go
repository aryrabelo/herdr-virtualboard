package dispatch

import (
	"context"
	"errors"
	"fmt"
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
//	tab   `ftr-0007`        stable per feature, reused across runs
//	 └─ anchor pane         a plain shell; the split parent
//	     └─ run pane        cwd = project root, env = the run's variables
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
	role, err := d.resolveRole(req, column)
	if err != nil {
		return nil, err
	}
	kind := d.resolveKind(req, column)
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

	if err := d.Store.Append(run); err != nil {
		return nil, err
	}

	if err := d.launch(ctx, req, run, role, column, kind); err != nil {
		if _, finishErr := d.Store.Finish(run.ID, runs.Failed, "dispatch_error", err.Error()); finishErr != nil {
			return nil, errors.Join(err, finishErr)
		}
		d.releaseLock(ctx, req.Spec.ID)
		return nil, err
	}
	return d.Store.Get(run.ID)
}

func (d *Dispatcher) launch(ctx context.Context, req Request, run *runs.Run, role roles.Role, column config.Column, kind string) error {
	// A worktree run already has its workspace: the linked one Herdr opened
	// for the checkout. Only a plain run has to find or create one.
	workspaceID := ""
	cwd := d.Workspace.Root
	if run.Worktree != nil {
		workspaceID, cwd = run.Worktree.WorkspaceID, run.Worktree.Path
	} else {
		var err error
		if workspaceID, err = d.resolveWorkspace(ctx); err != nil {
			return err
		}
	}

	env := Env(req.Spec, run.ID, role.Key, d.Workspace.Root, column)
	if run.Worktree != nil {
		// The agent works in the checkout, but the board it reports to is
		// still the one in the main repository, so the two are separate.
		env["HVB_WORKTREE"] = run.Worktree.Path
		env["HVB_BRANCH"] = run.Worktree.Branch
		env["HVB_BASE_BRANCH"] = run.Worktree.Base
	}

	tabID, anchorID, err := d.resolveTab(ctx, workspaceID, run, env, cwd)
	if err != nil {
		return err
	}
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
	agentName := runs.AgentName(run.FeatureID, run.ID)
	if _, err := d.Store.Update(run.ID, func(r *runs.Run) error {
		r.PaneID, r.AgentName, r.State = pane.PaneID, agentName, runs.Running
		return nil
	}); err != nil {
		return err
	}
	// Best-effort cosmetics: a pane that could not be relabelled still runs.
	_ = d.Herdr.RenamePane(ctx, pane.PaneID, runs.PaneLabel(run.FeatureID, role.Key))

	blocked, err := d.startAgent(ctx, agentName, kind, pane.PaneID)
	if err != nil {
		return fmt.Errorf("start %s agent: %w", kind, err)
	}

	// The anchor has done its job. Closing a split parent is safe — the
	// child keeps its process and environment — and leaves the user looking
	// at one pane per run instead of two.
	if err := d.Herdr.ClosePane(ctx, anchorID); err == nil {
		if _, updateErr := d.Store.Update(run.ID, func(r *runs.Run) error {
			r.AnchorPane = ""
			return nil
		}); updateErr != nil {
			return updateErr
		}
	}

	charter, err := role.Charter()
	if err != nil && role.Path != "" {
		// A missing charter degrades the prompt but must not abort a run
		// whose agent is already live in its pane.
		d.note(run.ID, fmt.Sprintf("role charter unreadable: %v", err))
		charter = ""
	}
	prompt := BuildPrompt(PromptInput{
		Spec:     req.Spec,
		Role:     role,
		Charter:  charter,
		Column:   column,
		RunID:    run.ID,
		RelPath:  d.Workspace.Rel(req.Spec.Path),
		Worktree: run.Worktree,
		WantPR:   run.PullRequest != nil,
	})
	return d.submitPrompt(ctx, run.ID, agentName, prompt, blocked)
}

// submitPrompt sends the task, deferring it while the harness is blocked.
//
// hvb never answers a startup dialog itself. Whether to trust a directory is
// the user's decision, and a board that clicks "yes" on their behalf would be
// making a security choice nobody asked it to make. Instead the prompt is
// parked on the run and Reconcile submits it once the agent settles.
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

// resolveWorkspace finds the Herdr workspace whose panes already run in the
// project root, creating one when none does.
func (d *Dispatcher) resolveWorkspace(ctx context.Context) (string, error) {
	if id, found, err := d.Herdr.WorkspaceForPath(ctx, d.Workspace.Root); err != nil {
		return "", fmt.Errorf("find workspace: %w", err)
	} else if found {
		return id, nil
	}
	created, _, _, err := d.Herdr.CreateWorkspace(ctx, d.Workspace.Root, d.Workspace.Name(), nil, false)
	if err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return created.WorkspaceID, nil
}

// resolveTab reuses the feature's existing tab when one is open, so repeated
// runs on a feature stay in one place. It returns the tab and an anchor pane
// inside it that is safe to split from.
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

// resolveRole picks the charter this dispatch runs under, or refuses the
// dispatch outright when the card is not an agent's to take.
//
// The refusal is consulted before req.Role and column.Role, which is a
// deliberate change of precedence and not an accident of ordering.
// roles.Suggest already decided that `hitl` beats the card's own `role:`
// label; a flag that outranked the refusal here would make the same card
// dispatchable or not depending on which layer answered, and that incoherence
// is worse than either rule alone. It is not hypothetical: config.Default
// gives the review column role = "qa", so a gate placed after the
// explicit-role branch would be bypassed by stock configuration for every
// `hitl` card that reaches review. The owner who means it anyway drops the
// label, which is the same act as saying the human half is done.
//
// roles.ErrNoCharter is not a refusal and keeps its fallback: a workspace with
// no agents directory still dispatches under the configured role, which is
// what roles.Load's doc promises.
func (d *Dispatcher) resolveRole(req Request, column config.Column) (roles.Role, error) {
	suggested, err := roles.Suggest(d.Roles, req.Spec, d.Config.Role)
	if errors.Is(err, roles.ErrHumanOnly) {
		return roles.Role{}, err
	}
	for _, want := range []string{req.Role, column.Role} {
		if want == "" {
			continue
		}
		if role, found := roles.Find(d.Roles, want); found {
			return role, nil
		}
	}
	if err != nil {
		return roles.Role{Key: d.Config.Role, Name: d.Config.Role}, nil
	}
	return suggested, nil
}

func (d *Dispatcher) resolveKind(req Request, column config.Column) string {
	for _, candidate := range []string{req.Kind, column.Harness, d.Config.Harness} {
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
