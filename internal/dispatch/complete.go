package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/netors/herdr-virtualboard/internal/feature"
	"github.com/netors/herdr-virtualboard/internal/herdrcli"
	"github.com/netors/herdr-virtualboard/internal/runs"
	"github.com/netors/herdr-virtualboard/internal/vb"
)

// Outcome is what a finished agent reports.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomeBlocked Outcome = "blocked"
)

// ParseOutcome resolves an outcome name.
func ParseOutcome(value string) (Outcome, bool) {
	switch Outcome(strings.ToLower(strings.TrimSpace(value))) {
	case OutcomeSuccess:
		return OutcomeSuccess, true
	case OutcomeFailure:
		return OutcomeFailure, true
	case OutcomeBlocked:
		return OutcomeBlocked, true
	default:
		return "", false
	}
}

// Completion is the result of finishing a run.
type Completion struct {
	Run *runs.Run `json:"run"`
	// PullRequest is the outcome of opening one, when the run asked for it.
	PullRequest *runs.PullRequest `json:"pull_request,omitempty"`
	// Moved is the status the feature was transitioned to, empty when the
	// column routed nowhere or the transition was refused.
	Moved feature.Status `json:"moved,omitempty"`
	// TransitionError explains a refused transition. The run still ends:
	// the agent did its work, and a routing rule the lifecycle forbids is a
	// configuration problem, not a reason to lose the outcome.
	TransitionError string `json:"transition_error,omitempty"`
}

// Complete ends a run, applies the column's routing, and releases the lock.
//
// It is the single place where an agent's report turns into a board change, and
// it is idempotent: a run that already ended keeps its first outcome.
func (d *Dispatcher) Complete(ctx context.Context, runID string, outcome Outcome, note string) (*Completion, error) {
	run, err := d.Store.Get(runID)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() {
		return &Completion{Run: run, Moved: feature.Status(run.MovedTo), PullRequest: run.PullRequest}, nil
	}

	state := runs.Succeeded
	if outcome != OutcomeSuccess {
		state = runs.Failed
	}
	if run, err = d.Store.Finish(runID, state, string(outcome), note); err != nil {
		return nil, err
	}

	// The pull request comes before the routing move. A reviewer arriving
	// from the board should find the branch already published, and the
	// spec's Links section is written here so the move that follows carries
	// it into the next column.
	if outcome == OutcomeSuccess && run.PullRequest != nil && !run.PullRequest.Opened {
		pr := d.openPullRequest(ctx, run)
		if updated, err := d.Store.Update(run.ID, func(r *runs.Run) error {
			r.PullRequest = pr
			return nil
		}); err == nil {
			run = updated
		}
		d.recordPullRequestLink(ctx, run, pr)
	}

	completion := &Completion{Run: run}
	target, ok := d.route(run, outcome)
	if !ok {
		d.releaseLock(ctx, run.FeatureID)
		completion.PullRequest = run.PullRequest
		return completion, nil
	}

	moved, err := d.transition(ctx, run, target)
	if err != nil {
		completion.TransitionError = err.Error()
		d.note(run.ID, completion.TransitionError)
		d.releaseLock(ctx, run.FeatureID)
		completion.PullRequest = run.PullRequest
		return completion, nil
	}
	completion.Moved = moved
	if _, err := d.Store.Update(run.ID, func(r *runs.Run) error {
		r.MovedTo = string(moved)
		return nil
	}); err != nil {
		return nil, err
	}
	d.releaseLock(ctx, run.FeatureID)
	completion.PullRequest = run.PullRequest
	return completion, nil
}

// route picks the destination status for a finished run. A `blocked` outcome
// goes to `blocked` when the lifecycle allows it, regardless of the column's
// failure route — an agent that says it is blocked is reporting a fact about
// the world, not a verdict on the work.
func (d *Dispatcher) route(run *runs.Run, outcome Outcome) (feature.Status, bool) {
	column := d.Config.Column(run.Status)
	var want string
	switch outcome {
	case OutcomeSuccess:
		want = column.OnSuccess
	case OutcomeBlocked:
		if feature.CanTransition(run.Status, feature.Blocked) {
			return feature.Blocked, true
		}
		want = column.OnFailure
	default:
		want = column.OnFailure
	}
	if want == "" {
		return "", false
	}
	status, ok := feature.ParseStatus(want)
	return status, ok
}

// transition moves the feature through vb, from wherever it actually is now.
//
// The run recorded the status it started from, but a human may have moved the
// feature while the agent worked. vb is re-read here so the transition is
// validated against the truth rather than against a stale assumption.
func (d *Dispatcher) transition(ctx context.Context, run *runs.Run, target feature.Status) (feature.Status, error) {
	spec, err := d.Workspace.FindSpec(run.FeatureID)
	if err != nil {
		return "", err
	}
	if spec.Status == target {
		return target, nil
	}
	if !feature.CanTransition(spec.Status, target) {
		return "", fmt.Errorf("cannot move %s from %s to %s: VirtualBoard does not allow that transition", run.FeatureID, spec.Status, target)
	}
	// Hand the feature back when it leaves an agent's hands, so the board
	// does not show work as owned by an agent that has stopped.
	owner := vb.ClearOwner
	if target == feature.InProgress {
		owner = d.Config.ResolveOwner()
	}
	if _, err := d.VB.Move(ctx, run.FeatureID, target, owner); err != nil {
		return "", err
	}
	// INDEX.md is generated, and a stale index is the most visible way a
	// board drifts from its repository.
	if err := d.VB.Index(ctx); err != nil {
		d.note(run.ID, fmt.Sprintf("index regeneration failed: %v", err))
	}
	return target, nil
}

// Cancel stops a run and closes its pane.
func (d *Dispatcher) Cancel(ctx context.Context, runID, note string) (*runs.Run, error) {
	run, err := d.Store.Get(runID)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() {
		return run, nil
	}
	// Pane cleanup stays ungated on purpose: hvb must always be able to tidy
	// up a pane it created, even against a Herdr it would refuse to dispatch
	// into.
	if run.PaneID != "" {
		if err := d.Herdr.ClosePane(ctx, run.PaneID); err != nil {
			d.note(run.ID, fmt.Sprintf("close pane %s: %v", run.PaneID, err))
		}
	}
	if run.AnchorPane != "" {
		_ = d.Herdr.ClosePane(ctx, run.AnchorPane)
	}
	d.releaseLock(ctx, run.FeatureID)
	return d.Store.Finish(runID, runs.Cancelled, "cancelled", note)
}

// Focus brings a run's pane into view.
func (d *Dispatcher) Focus(ctx context.Context, runID string) error {
	run, err := d.Store.Get(runID)
	if err != nil {
		return err
	}
	if run.PaneID == "" {
		return fmt.Errorf("run %s has no pane", runID)
	}
	if run.AgentName != "" {
		if err := d.Herdr.FocusAgent(ctx, run.AgentName); err == nil {
			return nil
		}
	}
	return d.Herdr.FocusPane(ctx, run.PaneID)
}

// Reconcile re-reads Herdr and settles every active run against what is really
// there. It is what makes the board honest without a daemon: the TUI calls it
// on each refresh, and the CLI calls it before listing runs.
//
// Three things can have happened to an active run since hvb last looked:
//
//   - its pane is gone — the user closed it, or the harness exited. The run
//     ended without a report; that is `awaiting`, not a failure.
//   - the harness reports `done` but no outcome arrived. Also `awaiting`: the
//     agent stopped and only a human knows whether it succeeded.
//   - the column's timeout elapsed. The run is failed and routed accordingly.
func (d *Dispatcher) Reconcile(ctx context.Context) ([]*runs.Run, error) {
	active, err := d.Store.Active()
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	panes, err := d.Herdr.Panes(ctx, "")
	if err != nil {
		// Without a pane list there is nothing trustworthy to reconcile
		// against; leaving runs alone is strictly better than guessing
		// they died.
		return nil, fmt.Errorf("reconcile: list panes: %w", err)
	}
	live := map[string]herdrcli.Pane{}
	for _, pane := range panes {
		live[pane.PaneID] = pane
	}
	agents, err := d.Herdr.Agents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: list agents: %w", err)
	}
	agentByPane := map[string]herdrcli.Agent{}
	for _, agent := range agents {
		agentByPane[agent.PaneID] = agent
	}

	var changed []*runs.Run
	for _, run := range active {
		updated, err := d.reconcileOne(ctx, run, live, agentByPane)
		if err != nil {
			return changed, err
		}
		if updated != nil {
			changed = append(changed, updated)
		}
	}
	return changed, nil
}

func (d *Dispatcher) reconcileOne(ctx context.Context, run *runs.Run, live map[string]herdrcli.Pane, agents map[string]herdrcli.Agent) (*runs.Run, error) {
	if run.ID != "" && run.ID == d.SelfRunID {
		// The agent running this very command owns this run. Whatever Herdr
		// says about its pane, it is alive: it is the thing asking.
		return nil, nil
	}
	if run.State == runs.Queued && run.PaneID == "" {
		// A queued run with no pane never got off the ground; leave it for
		// the dispatch path to finish or fail.
		return nil, nil
	}
	if _, alive := live[run.PaneID]; !alive {
		updated, err := d.awaiting(ctx, run, "pane_closed", "the run's pane is gone; no outcome was reported")
		return updated, err
	}
	if column := d.Config.Column(run.Status); column.Timeout.Duration() > 0 {
		if time.Since(run.StartedAt) > column.Timeout.Duration() {
			note := fmt.Sprintf("run exceeded the %s timeout for %s", column.Timeout.Duration(), run.Status)
			if _, err := d.Complete(ctx, run.ID, OutcomeFailure, note); err != nil {
				return nil, err
			}
			return d.Store.Get(run.ID)
		}
	}
	agent, ok := agents[run.PaneID]
	if !ok {
		return nil, nil
	}
	// A task parked because the harness was blocked at startup goes in as
	// soon as the agent is ready for input. This is what makes a worktree
	// dispatch self-heal after the user answers a trust prompt.
	if run.PendingPrompt != "" {
		switch agent.AgentStatus {
		case herdrcli.StatusIdle, herdrcli.StatusDone:
			return d.submitPending(ctx, run)
		}
		return nil, nil
	}
	if agent.AgentStatus == herdrcli.StatusDone && run.State == runs.Running {
		return d.awaiting(ctx, run, "agent_done", "the harness finished its turn without reporting an outcome")
	}
	return nil, nil
}

// submitPending sends a prompt that was parked while the harness was blocked.
func (d *Dispatcher) submitPending(ctx context.Context, run *runs.Run) (*runs.Run, error) {
	target := run.AgentName
	if target == "" {
		target = run.PaneID
	}
	if err := d.Herdr.PromptAgent(ctx, target, run.PendingPrompt, false, 0); err != nil {
		if herdrcli.IsAgentBlocked(err) {
			// Still at a dialog; try again on the next pass.
			return nil, nil
		}
		d.note(run.ID, fmt.Sprintf("could not submit the parked task: %v", err))
		return nil, nil
	}
	return d.Store.Update(run.ID, func(r *runs.Run) error {
		r.PendingPrompt = ""
		return nil
	})
}

// awaiting parks a run for human review without routing the feature. The
// feature keeps its status and its owner: a run that stopped unexplained has
// not earned a transition in either direction.
func (d *Dispatcher) awaiting(ctx context.Context, run *runs.Run, outcome, note string) (*runs.Run, error) {
	updated, err := d.Store.Update(run.ID, func(r *runs.Run) error {
		if r.State.Terminal() || r.State == runs.Awaiting {
			return nil
		}
		now := time.Now().UTC()
		r.State = runs.Awaiting
		r.EndedAt = &now
		r.Outcome = outcome
		r.Note = note
		return nil
	})
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	d.releaseLock(ctx, run.FeatureID)
	return updated, nil
}
