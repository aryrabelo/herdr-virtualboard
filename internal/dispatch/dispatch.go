package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/netors/herdr-virtualboard/internal/config"
	"github.com/netors/herdr-virtualboard/internal/feature"
	"github.com/netors/herdr-virtualboard/internal/herdrcli"
	"github.com/netors/herdr-virtualboard/internal/roles"
	"github.com/netors/herdr-virtualboard/internal/runs"
	"github.com/netors/herdr-virtualboard/internal/vb"
	"github.com/netors/herdr-virtualboard/internal/workspace"
)

// Dispatcher launches feature agents into Herdr panes.
type Dispatcher struct {
	Workspace *workspace.Workspace
	Config    *config.Config
	Herdr     *herdrcli.Client
	VB        *vb.Client
	Store     *runs.Store
	Roles     []roles.Role
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
	// need a running Herdr to be reported.
	column := d.Config.Column(req.Spec.Status)
	role := d.resolveRole(req, column)
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
	workspaceID, err := d.resolveWorkspace(ctx)
	if err != nil {
		return err
	}

	env := Env(req.Spec, run.ID, role.Key, d.Workspace.Root, column)

	tabID, anchorID, err := d.resolveTab(ctx, workspaceID, run, env)
	if err != nil {
		return err
	}
	if _, err := d.Store.Update(run.ID, func(r *runs.Run) error {
		r.WorkspaceID, r.TabID, r.AnchorPane = workspaceID, tabID, anchorID
		return nil
	}); err != nil {
		return err
	}

	pane, err := d.Herdr.SplitPane(ctx, anchorID, "right", d.Workspace.Root, env, req.Focus)
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

	if err := d.startAgent(ctx, agentName, kind, pane.PaneID); err != nil {
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
		Spec:    req.Spec,
		Role:    role,
		Charter: charter,
		Column:  column,
		RunID:   run.ID,
		RelPath: d.Workspace.Rel(req.Spec.Path),
	})
	if err := d.Herdr.PromptAgent(ctx, agentName, prompt, false, 0); err != nil {
		return fmt.Errorf("submit prompt: %w", err)
	}
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
func (d *Dispatcher) resolveTab(ctx context.Context, workspaceID string, run *runs.Run, env map[string]string) (tabID, anchorID string, err error) {
	label := runs.TabLabel(run.FeatureID)
	tabs, err := d.Herdr.Tabs(ctx, workspaceID)
	if err != nil {
		return "", "", fmt.Errorf("list tabs: %w", err)
	}
	for _, tab := range tabs {
		if tab.Label != label {
			continue
		}
		anchor, err := d.anchorInTab(ctx, tab.TabID, run, env)
		if err != nil {
			return "", "", err
		}
		return tab.TabID, anchor, nil
	}

	tab, root, err := d.Herdr.CreateTab(ctx, workspaceID, d.Workspace.Root, label, env, false)
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
func (d *Dispatcher) anchorInTab(ctx context.Context, tabID string, run *runs.Run, env map[string]string) (string, error) {
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
		anchor, err := d.Herdr.SplitPane(ctx, pane.PaneID, "down", d.Workspace.Root, env, false)
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

func (d *Dispatcher) resolveRole(req Request, column config.Column) roles.Role {
	for _, want := range []string{req.Role, column.Role} {
		if want == "" {
			continue
		}
		if role, found := roles.Find(d.Roles, want); found {
			return role
		}
	}
	role, found := roles.Suggest(d.Roles, req.Spec, d.Config.Role)
	if !found {
		return roles.Role{Key: d.Config.Role, Name: d.Config.Role}
	}
	return role
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
func (d *Dispatcher) startAgent(ctx context.Context, name, kind, paneID string) error {
	deadline := time.Now().Add(paneReadyWait)
	var lastErr error
	for attempt := 0; ; attempt++ {
		lastErr = d.Herdr.StartAgent(ctx, name, kind, paneID, d.Config.StartTimeout.Duration(), nil)
		if lastErr == nil || !herdrcli.IsPaneBusy(lastErr) {
			return lastErr
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w (the pane never reached an interactive prompt within %s)", lastErr, paneReadyWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
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
