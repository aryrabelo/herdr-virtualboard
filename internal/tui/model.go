package tui

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/dispatch"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// Backend is everything the board reads and mutates. It is an interface so the
// model can be driven in tests without a VirtualBoard workspace, a vb binary,
// or a running Herdr.
type Backend interface {
	Load(ctx context.Context) ([]*feature.Spec, []*runs.Run, []error)
	Move(ctx context.Context, id string, target feature.Status, owner string) error
	Create(ctx context.Context, title string, labels []string, priority string) (string, error)
	SetField(ctx context.Context, id, key, value string) error
	Dispatch(ctx context.Context, spec *feature.Spec, role, kind string, force bool) (*runs.Run, error)
	// DispatchWith is Dispatch with explicit worktree and pull-request
	// intent; nil for either inherits configuration.
	DispatchWith(ctx context.Context, spec *feature.Spec, role, kind string, worktree, pr *bool) (*runs.Run, error)
	Cancel(ctx context.Context, runID string) error
	Focus(ctx context.Context, runID string) error
	// Workflow is the line the board draws: the columns, left to right,
	// and the moves each one allows. It comes from the backend because the
	// two boards do not agree — the spec board is vb's five states, and a
	// queue board draws whatever `[workflow] columns` declared.
	Workflow() *feature.Workflow
	Roles() []roles.Role
	Config() *config.Config
	Owner() string
	ProjectName() string
}

// Card is one feature as the board shows it: the spec plus the run context that
// lives outside the repository.
type Card struct {
	Spec   *feature.Spec
	Runs   []*runs.Run
	Active *runs.Run
}

// View is which screen the board is showing.
type View int

const (
	ViewBoard View = iota
	ViewDetail
	ViewHelp
	ViewNewFeature
	ViewMovePicker
	ViewDispatchPicker
	ViewConfirm
)

// Model is the board's whole state. Every render is a pure function of it.
type Model struct {
	backend Backend
	palette Palette

	width  int
	height int

	view View

	// workflow is the line this board draws, read from the backend once: a
	// backend's line is fixed for the life of the board, and asking again
	// on every column of every frame would buy nothing.
	workflow *feature.Workflow

	// columns holds the cards per column, in board order.
	columns map[feature.Status][]*Card
	// undeclared are the columns cards actually arrived in that the
	// workflow never declared, in a stable order. Every reader of the board
	// walks columnOrder, so a status missing from it is a card the board
	// holds and draws nowhere — a non-empty queue reporting itself empty.
	// They are therefore drawn after the declared columns and named in the
	// problems list: the line a repository declared is likelier to be wrong
	// than the forge the card came from, and either way the reader can see
	// the card. Recomputed by Reload, the only thing that can change them.
	undeclared []feature.Status
	// column and card are the focused positions.
	column int
	card   map[feature.Status]int

	problems []error
	status   string
	statusAt time.Time
	isError  bool
	// emptyHint is what the caller wants said when the board holds nothing.
	// A blank board is ambiguous — a clean queue and a source that produced
	// nothing look identical — and only the command that opened it knows
	// where the work would otherwise live.
	emptyHint string
	// focusColumn is the column name the caller wants the first load to
	// open on, or "" for wherever the work is. Only the command knows:
	// `hvb queue --focus-column ready-to-review` is a board opened to
	// answer one question rather than a board to browse. It is the raw
	// request, resolved through the workflow's own parser at load time.
	focusColumn string

	detailScroll  int
	detailSection int

	form    *form
	picker  *picker
	confirm *confirmation

	lastLoad time.Time
	// reloading is set while a background read is out, so the header can say
	// so and a refresh that falls due meanwhile can be dropped.
	reloading bool
	// loads counts the reads folded into the board, background or
	// synchronous. It is the generation a background read carries, so one
	// that finished behind a fresher read is recognisable as stale.
	loads    reloadToken
	quitting bool
}

// NewModel builds a board over a backend.
func NewModel(backend Backend, palette Palette) *Model {
	workflow := backend.Workflow()
	if workflow == nil {
		workflow = feature.VB()
	}
	return &Model{
		backend:  backend,
		palette:  palette,
		workflow: workflow,
		columns:  map[feature.Status][]*Card{},
		card:     map[feature.Status]int{},
		view:     ViewBoard,
		width:    80,
		height:   24,
	}
}

// columnOrder is the board's columns, left to right: the workflow's own, minus
// any column declared hidden while empty, plus any column cards arrived in
// that the workflow never declared.
//
// The hidden-while-empty column is the cancelled tail. A board over a
// VirtualBoard workspace, where no card is ever cancelled, looks exactly as it
// did before the column existed.
//
// The undeclared tail is the opposite case and the reason this is the board's
// only column order: a source answers with the column IT derived, and a
// declared line need not have it — the twelve-column line of ceo-bora#321 has
// no `blocked`, which internal/fios mints. Every reader here walks this slice,
// so leaving that status out drew none of its cards and let a queue with work
// in it report itself empty. Drawn last, and Reload also files a problem
// naming the missing column.
func (m *Model) columnOrder() []feature.Status {
	declared := m.workflow.Columns()
	hidden := 0
	for _, status := range declared {
		if m.workflow.HideWhenEmpty(status) && len(m.columns[status]) == 0 {
			hidden++
		}
	}
	if hidden == 0 && len(m.undeclared) == 0 {
		return declared
	}
	// Built fresh rather than appended to: `declared` is the workflow's own
	// slice, and appending into its spare capacity would rewrite the line
	// every board in the process draws.
	order := make([]feature.Status, 0, len(declared)-hidden+len(m.undeclared))
	for _, status := range declared {
		if m.workflow.HideWhenEmpty(status) && len(m.columns[status]) == 0 {
			continue
		}
		order = append(order, status)
	}
	return append(order, m.undeclared...)
}

// undeclaredColumns are the columns holding cards that the workflow does not
// declare, sorted so two loads of the same board draw them in the same order.
func (m *Model) undeclaredColumns() []feature.Status {
	var extra []feature.Status
	for status, cards := range m.columns {
		if len(cards) == 0 || m.workflow.Has(status) {
			continue
		}
		extra = append(extra, status)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return extra
}

// undeclaredProblems is one problem per undeclared column. The cards draw
// either way — columnOrder says why — but a column nobody declared is a
// configuration error, and the board is where it becomes visible.
func (m *Model) undeclaredProblems() []error {
	if len(m.undeclared) == 0 {
		return nil
	}
	out := make([]error, 0, len(m.undeclared))
	for _, status := range m.undeclared {
		out = append(out, fmt.Errorf(
			"%d card(s) arrived in %q, which the line does not declare: the board draws that column after the declared ones so none of them is lost — add %q to `[workflow] columns`, or fix the source reporting it",
			len(m.columns[status]), status, status))
	}
	return out
}

// Resize records new terminal geometry.
func (m *Model) Resize(width, height int) {
	m.width, m.height = width, height
}

// reloadToken names the board generation a background read was taken from, so
// a result that finished after the board already moved on can be recognised
// and dropped instead of applied.
type reloadToken uint64

// loadResult is the raw output of one backend read, on its way from the reload
// goroutine back to the event loop.
//
// It carries data only, and deliberately: the reload goroutine never touches
// the Model. Every field the renderer reads is still written on the event
// loop's own goroutine, which is what keeps the board free of data races
// without a mutex between the reader of every frame and the writer of every
// card.
type loadResult struct {
	token    reloadToken
	specs    []*feature.Spec
	runs     []*runs.Run
	problems []error
}

// Reloading reports whether a background read is in flight. The board says so
// in the header: a read can take a minute on a real project, and a board that
// looks idle while it waits is indistinguishable from a board that is stuck.
func (m *Model) Reloading() bool { return m.reloading }

// BeginReload claims the right to start one background read and returns the
// token its result must carry. It refuses in two cases.
//
// An overlay is open: reloading under a form would fight with what the user is
// typing.
//
// A read is already in flight: one reload shells out to `vb`, `gh` and the
// project's own kit — measured at 69 seconds for a single `kit.py fronteira`
// on the owner's board — while the refresh tick is 5 seconds. The tick is
// DROPPED, never queued. Queueing would grow an unbounded backlog of reads
// whose answers are already stale when they arrive, and the board would spend
// the rest of the session catching up with itself.
func (m *Model) BeginReload() (reloadToken, bool) {
	if m.reloading || !m.reloadable() {
		return 0, false
	}
	m.reloading = true
	return m.loads, true
}

// Load performs the backend read for a background reload. It is the only
// method meant to be called off the event loop's goroutine: it reads
// m.backend, which is fixed for the model's lifetime, and nothing else.
func (m *Model) Load(ctx context.Context, token reloadToken) loadResult {
	specs, allRuns, problems := m.backend.Load(ctx)
	return loadResult{token: token, specs: specs, runs: allRuns, problems: problems}
}

// ApplyReload folds a finished background read into the board, or drops it.
//
// It is dropped when the board has moved since the read was taken, which is
// the same rule BeginReload applies, now seen from the far end of a read that
// may have been out for a minute:
//
//   - An overlay opened while the read was out. The overlays hold references
//     into the cards they were opened over and the form holds what the user
//     typed; swapping the board underneath would act on a card that moved.
//   - The board was reloaded in the meantime, by `r` or by the reload every
//     mutation in update.go does after itself. The result describes the board
//     as it was *before* the user's move, so applying it would visibly undo
//     the move they just watched succeed.
//
// Either way the next tick starts a fresh read, so nothing is lost but a stale
// answer.
func (m *Model) ApplyReload(result loadResult) {
	m.reloading = false
	if !m.reloadable() || result.token != m.loads {
		return
	}
	m.apply(result)
}

// reloadable reports whether the board is in a state a background reload may
// touch: the two views that are only showing what the board already knows.
func (m *Model) reloadable() bool {
	return m.view == ViewBoard || m.view == ViewDetail
}

// Reload refreshes the board from the backend synchronously. It is what an
// explicit request means — `r`, or the refresh a mutation does to see its own
// effect — where the user is waiting for this particular answer and a later
// one would be the wrong one.
func (m *Model) Reload(ctx context.Context) {
	m.apply(m.Load(ctx, m.loads))
}

// apply installs a completed read, preserving the focused card by identity
// rather than by index: a refresh that silently moves the selection because
// another agent finished a feature is how a user dispatches the wrong thing.
func (m *Model) apply(result loadResult) {
	focused := m.FocusedCard()
	var focusedID string
	if focused != nil {
		focusedID = focused.Spec.ID
	}

	runsByFeature := map[string][]*runs.Run{}
	for _, run := range result.runs {
		runsByFeature[run.FeatureID] = append(runsByFeature[run.FeatureID], run)
	}

	columns := map[feature.Status][]*Card{}
	for _, spec := range result.specs {
		card := &Card{Spec: spec, Runs: runsByFeature[spec.ID]}
		for _, run := range card.Runs {
			if run.Active() {
				card.Active = run
				break
			}
		}
		columns[spec.Status] = append(columns[spec.Status], card)
	}
	m.columns = columns
	m.undeclared = m.undeclaredColumns()
	m.problems = append(result.problems, m.undeclaredProblems()...)
	first := m.lastLoad.IsZero()
	m.lastLoad = time.Now()
	m.loads++

	m.clampSelection()
	switch {
	case focusedID != "":
		m.focusFeature(focusedID)
	case first:
		// The caller may have named the column to open on — that is how
		// `prefix+k` lands on Ready To Review. Failing that, opening on
		// an empty backlog while every card is in review makes the user
		// navigate before the board tells them anything, so start where
		// the work is.
		if !m.focusColumnNamed(m.focusColumn) {
			m.focusFirstPopulatedColumn()
		}
	}
}

// focusColumnNamed moves the selection to the column the caller named,
// reporting whether the board draws it. A column the workflow does not declare
// — or one hidden because it is empty — is not an error: the caller asked for a
// place to open and the board falls back to where the work is.
//
// The name is resolved through the workflow's own parser rather than compared
// raw. `--focus-column` carries whatever the shell or the keybinding had, and
// every other column name in hvb accepts `ready_to_review` and
// `Ready-To-Review` for `ready-to-review`; comparing the raw text made those
// spellings fall back silently, which on screen is indistinguishable from a
// line that does not declare the column at all. An empty request parses as no
// column, which is what "open wherever the work is" already meant.
func (m *Model) focusColumnNamed(name string) bool {
	status, ok := m.workflow.Parse(name)
	if !ok {
		return false
	}
	for index, candidate := range m.columnOrder() {
		if candidate == status {
			m.column = index
			return true
		}
	}
	return false
}

// focusFirstPopulatedColumn moves the selection to the leftmost column that has
// cards, leaving it alone when the board is empty.
func (m *Model) focusFirstPopulatedColumn() {
	for index, status := range m.columnOrder() {
		if len(m.columns[status]) > 0 {
			m.column = index
			return
		}
	}
}

// focusFeature moves the selection to a feature wherever it now lives.
func (m *Model) focusFeature(id string) bool {
	for columnIndex, status := range m.columnOrder() {
		for cardIndex, card := range m.columns[status] {
			if card.Spec.ID == id {
				m.column = columnIndex
				m.card[status] = cardIndex
				return true
			}
		}
	}
	return false
}

// clampSelection keeps the focused indices inside the board after a reload.
func (m *Model) clampSelection() {
	if m.column < 0 {
		m.column = 0
	}
	order := m.columnOrder()
	if m.column >= len(order) {
		m.column = len(order) - 1
	}
	for _, status := range order {
		count := len(m.columns[status])
		index := m.card[status]
		switch {
		case count == 0:
			m.card[status] = 0
		case index >= count:
			m.card[status] = count - 1
		case index < 0:
			m.card[status] = 0
		}
	}
}

// FocusedStatus is the status of the focused column. An out-of-range selection
// answers with the leftmost column the board draws rather than a vb state the
// declared line may not even have.
func (m *Model) FocusedStatus() feature.Status {
	order := m.columnOrder()
	if len(order) == 0 {
		// Every column hides while empty and every column is empty. The
		// workflow still has a first column, and the board has to name
		// somewhere.
		return m.workflow.Columns()[0]
	}
	if m.column < 0 || m.column >= len(order) {
		return order[0]
	}
	return order[m.column]
}

// FocusedCard is the selected card, or nil when the column is empty.
func (m *Model) FocusedCard() *Card {
	status := m.FocusedStatus()
	cards := m.columns[status]
	index := m.card[status]
	if index < 0 || index >= len(cards) {
		return nil
	}
	return cards[index]
}

// Cards returns a column's cards.
func (m *Model) Cards(status feature.Status) []*Card { return m.columns[status] }

// Counts returns the number of cards per status.
func (m *Model) Counts() map[feature.Status]int {
	out := map[feature.Status]int{}
	for _, status := range m.columnOrder() {
		out[status] = len(m.columns[status])
	}
	return out
}

// Empty reports whether a completed load produced no cards at all. It is
// false before the first load: a board that has not read anything yet has not
// found nothing, and saying so would be the same lie in the other direction.
func (m *Model) Empty() bool {
	if m.lastLoad.IsZero() {
		return false
	}
	for _, status := range m.columnOrder() {
		if len(m.columns[status]) > 0 {
			return false
		}
	}
	return true
}

// ActiveRunCount is how many runs currently hold a pane.
func (m *Model) ActiveRunCount() int {
	count := 0
	for _, status := range m.columnOrder() {
		for _, card := range m.columns[status] {
			if card.Active != nil {
				count++
			}
		}
	}
	return count
}

// Quitting reports whether the event loop should stop.
func (m *Model) Quitting() bool { return m.quitting }

// setStatus posts a transient message to the footer.
func (m *Model) setStatus(format string, args ...any) {
	m.status, m.isError, m.statusAt = fmt.Sprintf(format, args...), false, time.Now()
}

func (m *Model) setError(err error) {
	if err == nil {
		return
	}
	m.status, m.isError, m.statusAt = err.Error(), true, time.Now()
}

// statusMessage returns the footer message, expiring it after a while so a
// stale "dispatched" does not sit under a board that has moved on.
func (m *Model) statusMessage() (string, bool) {
	if m.status == "" || time.Since(m.statusAt) > statusTTL {
		return "", false
	}
	return m.status, m.isError
}

const statusTTL = 12 * time.Second

// sortRunsNewestFirst is used by the detail view, which shows history in the
// order a human reads it.
func sortRunsNewestFirst(list []*runs.Run) []*runs.Run {
	out := append([]*runs.Run(nil), list...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// liveBackend is the production Backend, over a real workspace.
type liveBackend struct {
	workspace  *workspace.Workspace
	config     *config.Config
	vb         *vb.Client
	herdr      *herdrcli.Client
	store      *runs.Store
	dispatcher *dispatch.Dispatcher
	roles      []roles.Role
}

// NewBackend builds the production backend.
func NewBackend(
	ws *workspace.Workspace,
	cfg *config.Config,
	vbClient *vb.Client,
	herdrClient *herdrcli.Client,
	store *runs.Store,
	dispatcher *dispatch.Dispatcher,
	loadedRoles []roles.Role,
) Backend {
	return &liveBackend{
		workspace: ws, config: cfg, vb: vbClient, herdr: herdrClient,
		store: store, dispatcher: dispatcher, roles: loadedRoles,
	}
}

func (b *liveBackend) Load(ctx context.Context) ([]*feature.Spec, []*runs.Run, []error) {
	specs, problems := b.workspace.LoadSpecs()
	// Reconciling before reading is what keeps the board honest without a
	// daemon: a run whose pane closed is settled here, not left showing as
	// live until someone notices.
	if _, err := b.dispatcher.Reconcile(ctx); err != nil {
		problems = append(problems, err)
	}
	allRuns, err := b.store.List()
	if err != nil {
		problems = append(problems, err)
	}
	return specs, allRuns, problems
}

func (b *liveBackend) Move(ctx context.Context, id string, target feature.Status, owner string) error {
	if _, err := b.vb.Move(ctx, id, target, owner); err != nil {
		return err
	}
	if err := b.vb.Index(ctx); err != nil {
		return fmt.Errorf("moved %s, but the index is stale: %w", id, err)
	}
	return nil
}

func (b *liveBackend) Create(ctx context.Context, title string, labels []string, priority string) (string, error) {
	created, err := b.vb.New(ctx, title, labels...)
	if err != nil {
		return "", err
	}
	if priority != "" {
		if err := b.vb.Update(ctx, created.ID, map[string]string{"priority": priority}, nil); err != nil {
			return created.ID, err
		}
	}
	if err := b.vb.Index(ctx); err != nil {
		return created.ID, fmt.Errorf("created %s, but the index is stale: %w", created.ID, err)
	}
	return created.ID, nil
}

func (b *liveBackend) SetField(ctx context.Context, id, key, value string) error {
	if err := b.vb.Update(ctx, id, map[string]string{key: value}, nil); err != nil {
		return err
	}
	if err := b.vb.Index(ctx); err != nil {
		return fmt.Errorf("updated %s, but the index is stale: %w", id, err)
	}
	return nil
}

func (b *liveBackend) Dispatch(ctx context.Context, spec *feature.Spec, role, kind string, force bool) (*runs.Run, error) {
	return b.dispatcher.Start(ctx, dispatch.Request{Spec: spec, Role: role, Kind: kind, Force: force})
}

func (b *liveBackend) DispatchWith(ctx context.Context, spec *feature.Spec, role, kind string, worktree, pr *bool) (*runs.Run, error) {
	return b.dispatcher.Start(ctx, dispatch.Request{
		Spec: spec, Role: role, Kind: kind, Worktree: worktree, PR: pr,
	})
}

func (b *liveBackend) Cancel(ctx context.Context, runID string) error {
	_, err := b.dispatcher.Cancel(ctx, runID, "cancelled from the board")
	return err
}

func (b *liveBackend) Focus(ctx context.Context, runID string) error {
	return b.dispatcher.Focus(ctx, runID)
}

// Workflow is vb's lifecycle, always. The spec board deliberately does not read
// `[workflow] columns`: every card here is spec markdown vb owns, and a column
// vb cannot move a spec into would be a column whose every move fails.
func (b *liveBackend) Workflow() *feature.Workflow { return feature.VB() }

func (b *liveBackend) Roles() []roles.Role    { return b.roles }
func (b *liveBackend) Config() *config.Config { return b.config }
func (b *liveBackend) Owner() string          { return b.config.ResolveOwner() }
func (b *liveBackend) ProjectName() string    { return b.workspace.Name() }
