package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/colunas"
	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/dispatch"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/linha"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// Source is one read-only card source: it produces cards and reports the
// problems it hit, and it never writes to the files it read.
//
// The interface is declared here rather than imported so composing a board
// costs nothing in dependencies: internal/fios and internal/ghboard satisfy it
// structurally, and a test satisfies it with a literal.
type Source interface {
	Load(ctx context.Context) ([]*feature.Spec, []error)
}

// LabelPrefix marks a label a source minted, as opposed to one the repository
// or the owner's file merely says. It is reserved, and the distinction is not
// cosmetic: a card's labels mix two authorities, and a pull request carries its
// repository's real labels verbatim. A repository can own a label literally
// named `state:canceled` or `external`, so a board deriving a column or a badge
// from an unprefixed name would be taking dictation from content anyone can
// edit in the GitHub UI. The sources drop any incoming label that spells the
// prefix, so the namespace stays the harness's own.
const LabelPrefix = "hvb:"

// Labels every source agrees on. The board reads facts off the card instead of
// asking the source about it, so a card carries everything the board needs to
// draw it and to refuse to edit it.
//
// They are spelled here rather than imported from internal/fios and
// internal/ghboard, because depending on those packages is exactly what the
// Source interface exists to avoid: the board composes whatever satisfies it.
// The two spellings cannot drift apart silently — a test in this package reads
// the sources' own exported constants and fails if either side renames one.
const (
	LabelSourceFios  = LabelPrefix + "source:fios"
	LabelSourceGates = LabelPrefix + "source:gates"
	LabelSourcePR    = LabelPrefix + "source:pr"
	LabelSourceIssue = LabelPrefix + "source:issue"
	LabelCanceled    = LabelPrefix + "state:canceled"
	LabelExternal    = LabelPrefix + "external"
	LabelDraft       = LabelPrefix + "draft"

	labelIssuePrefix = LabelPrefix + "issue:"
	labelPRPrefix    = LabelPrefix + "pr:"
	labelGatePrefix  = LabelPrefix + "gate:"
)

// ErrReadOnly marks a mutation the board refused because the card's source owns
// the file. The message names the owning file and the tool allowed to edit it;
// a silent no-op would leave the user pressing a key that appears to work.
var ErrReadOnly = errors.New("read-only source")

// IssueDispatcher launches an agent on a GitHub issue card.
//
// It is an interface, and narrow on purpose: the usina owns the worktree, the
// bora workspace and the scoped token a real dispatch needs, and internal/tui
// must not import that seam to draw a board. The board hands over a card and
// gets back the sentence to show.
type IssueDispatcher interface {
	DispatchIssue(ctx context.Context, spec *feature.Spec) (string, error)
}

// Line is the production-line half of a sources board: the declared policy,
// the store holding the columns the forge has no fact for, the per-repository
// quiet windows, and the despatcher an issue card uses.
//
// A board without one is the board `hvb queue` has always been — every column
// comes from the source and every write is refused. That is not a degraded
// mode: `hvb queue --vault` reads FIOS.md, which has no repository, no pull
// request and no quiet window to measure.
type Line struct {
	// Policy is the declared line as internal/linha reads it.
	Policy linha.Policy
	// Store records a move into a column no fact decides. Nil refuses the
	// move and says so, because a key that appears to work and then forgets
	// is worse than one that explains itself.
	Store *colunas.Store
	// Timer answers a repository's quiet window, and answers false when the
	// repository declared none. Nil holds every pull request with that same
	// reason rather than inventing a window.
	Timer func(repo string) (time.Duration, bool)
	// PRRepo and IssueRepo name the repositories the two forge sources
	// read, so a card is charged to the right quiet window and a stored
	// entry can say which repository it belongs to.
	PRRepo    string
	IssueRepo string
	// Issues hands an issue card to `usina agente dispatch`. Nil leaves
	// issue cards on the ordinary refusal.
	Issues IssueDispatcher
	// Now is the clock the quiet window is measured against; nil is
	// time.Now, and a test pins it.
	Now func() time.Time
}

// repoOf is the repository a card is charged to. A pull request belongs to the
// repository it was opened in; everything else to the issue queue the board was
// pointed at, which is where its card came from.
func (l *Line) repoOf(spec *feature.Spec) string {
	if spec != nil && spec.HasLabel(LabelSourcePR) {
		return l.PRRepo
	}
	return l.IssueRepo
}

// clock is Now, or the real one.
func (l *Line) clock() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// SourceBackend is a Backend over read-only sources: the owner's own queue
// (FIOS.md, gates/) and the week's pull requests, rather than VirtualBoard spec
// markdown. It reads, it draws, and it refuses every write with an explanation.
type SourceBackend struct {
	sources []Source
	config  *config.Config
	owner   string
	project string

	// workflow is the line this board draws. It is handed in rather than
	// read from config here so the caller decides which board it is
	// opening: `hvb queue` renders the declared line, and a test renders
	// whatever it is testing.
	workflow *feature.Workflow
	// dispatcher launches an agent on a card, or is nil on a board that
	// only reads. Both shapes are real: `hvb queue` without --charters and
	// --work-root has no repository to work in and no charters to pick a
	// role from, and it must keep behaving exactly as it did — refusing
	// with the source's own next step rather than half-dispatching.
	dispatcher *dispatch.Dispatcher
	// line is the production line, or nil on a board that only reflects its
	// sources. Nil is the shape this backend shipped with and every
	// behaviour of it is still reachable, so the two are tested apart.
	line *Line

	// origins remembers, per card, the sentence that says where it lives, so
	// a refusal can name the file without going back to the sources. kinds is
	// the same thing for the board as a whole, for messages that cannot name
	// a single card. verdicts is the line's last answer per card, which is
	// what lets Move refuse a write the next Load would overrule, and numbers
	// is the issue number read off the card's own label — an id is an opaque
	// string a source may mint from a hash, so it is never parsed for one.
	// repos is the repository each card is charged to, measured the same way
	// resolveColumns picks its quiet window: a stored entry says which
	// repository it belongs to, and a pull request's is not the issue
	// queue's whenever the board reads two of them.
	mu       sync.Mutex
	origins  map[string]string
	kinds    []string
	verdicts map[string]linha.Verdict
	numbers  map[string]int
	repos    map[string]string
}

// WithLine turns a sources board into a production line. It is a separate step
// rather than a constructor parameter because the two boards `hvb queue` opens
// differ by exactly this: a vault board has no line to give it.
func (b *SourceBackend) WithLine(line *Line) *SourceBackend {
	b.line = line
	return b
}

// NewSourceBackend composes already-built sources into a board. Sources are
// injected rather than constructed from paths so the board is testable without
// a vault on disk and without the gh binary.
//
// A nil workflow falls back to the vb lifecycle, which is what a board with no
// declared line has always drawn.
func NewSourceBackend(project string, cfg *config.Config, wf *feature.Workflow, owner string, sources ...Source) *SourceBackend {
	if cfg == nil {
		defaults := config.Default()
		cfg = &defaults
	}
	if wf == nil {
		wf = feature.VB()
	}
	if owner == "" {
		owner = cfg.ResolveOwner()
	}
	if project == "" {
		project = "queue"
	}
	return &SourceBackend{
		sources: sources, config: cfg, workflow: wf, owner: owner, project: project,
		origins: map[string]string{},
	}
}

// NewSourceBackendWithDispatch is NewSourceBackend plus the ability to launch
// an agent on a card. The dispatcher is injected fully built — internal/cli
// already assembles one (app.go:140-150) and is the only place that knows how
// to reach Herdr, vb and the run store — so this package gains no dependency
// it did not already have, and the board keeps working without any of them.
//
// The dispatcher's configuration is replaced by a copy with the vb lock off;
// withoutVBLock says why that is not optional.
func NewSourceBackendWithDispatch(project string, cfg *config.Config, wf *feature.Workflow, owner string, dispatcher *dispatch.Dispatcher, sources ...Source) *SourceBackend {
	backend := NewSourceBackend(project, cfg, wf, owner, sources...)
	if dispatcher == nil {
		return backend
	}
	// The Dispatcher is copied too: overwriting the caller's Config field
	// would hand back a value changed behind its back, and the caller may
	// well be building a second dispatcher from the same config.
	dispatching := *dispatcher
	source := dispatcher.Config
	if source == nil {
		source = backend.config
	}
	dispatching.Config = withoutVBLock(source)
	backend.dispatcher = &dispatching
	return backend
}

// withoutVBLock is cfg with the vb lock disabled, as a copy.
//
// A card on this board is a pull request, a GitHub issue, a FIOS.md thread or
// a gate box, and vb has never heard of any of those ids. claim calls
// vb.Acquire on the id and only skips it when the TTL is non-positive
// (dispatch.go:363-366), while the default is 60 minutes (config.go:209) — so
// with the shared setting every dispatch from here would die on a lock for a
// feature that does not exist.
//
// It has to be a copy, and that is the easy part to get wrong. The same
// *config.Config is what the board itself reads for its columns and its
// harness, and what `hvb tui` hands the vb backend in the same process;
// zeroing the field in place would silently turn locking off for every feature
// that pointer reaches. Only the one scalar is overridden, so the nested
// Columns map stays shared — dispatch only reads it.
func withoutVBLock(cfg *config.Config) *config.Config {
	unlocked := *cfg
	unlocked.LockTTLMinutes = 0
	return &unlocked
}

// Load reads every source. A source that fails contributes its errors and
// whatever it did manage to read; it never stops the others, because a board
// that goes blank when GitHub is unreachable has hidden the owner's own queue
// for a reason that has nothing to do with it.
func (b *SourceBackend) Load(ctx context.Context) ([]*feature.Spec, []*runs.Run, []error) {
	var (
		specs    []*feature.Spec
		problems []error
	)
	for _, source := range b.sources {
		if source == nil {
			continue
		}
		loaded, errs := source.Load(ctx)
		specs = append(specs, loaded...)
		problems = append(problems, errs...)
	}

	verdicts, lineProblems := b.resolveColumns(specs)
	problems = append(problems, lineProblems...)

	origins := make(map[string]string, len(specs))
	numbers := make(map[string]int, len(specs))
	repos := make(map[string]string, len(specs))
	var kinds []string
	for _, spec := range specs {
		origins[spec.ID] = describeOrigin(spec)
		if number := issueNumber(spec); number > 0 {
			numbers[spec.ID] = number
		}
		// Read off the labels, exactly as resolveColumns reads them for
		// the quiet window: a card's repository is a property of the
		// source that built it, and Move only ever gets an id back.
		if b.line != nil {
			repos[spec.ID] = b.line.repoOf(spec)
		}
		if kind := sourceKind(spec); kind != "" && !contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	b.mu.Lock()
	b.origins, b.kinds, b.verdicts, b.numbers, b.repos = origins, kinds, verdicts, numbers, repos
	b.mu.Unlock()

	// Runs come from the dispatcher's own store. A read-only board records
	// none, and a dispatching one reads back exactly what it launched: the
	// store file is keyed per board (see queueProjectID in internal/cli), so
	// nothing else writes to it.
	if b.dispatcher == nil {
		return specs, nil, problems
	}
	// Reconciling before reading is what keeps the board honest without a
	// daemon, as on the vb board: a run whose pane the user closed settles
	// here instead of showing as live forever. Its timeout route ends in
	// Complete, whose vb transition cannot land for these ids — FindSpec
	// looks under <work-root>/.virtualboard, which a plain repository does
	// not have — but Complete records that as the run's TransitionError and
	// still ends the run, so the run settles and the board says why.
	if _, err := b.dispatcher.Reconcile(ctx); err != nil {
		problems = append(problems, err)
	}
	dispatched, err := b.dispatcher.Store.List()
	if err != nil {
		problems = append(problems, err)
	}
	return specs, dispatched, problems
}

// resolveColumns puts every card in its column and records why. It is the
// line's whole read path.
//
// The specs the sources just built are mutated in place, and that is the
// design rather than a shortcut: a card's column is not a property of its
// source. The source only offers a floor, a stored move can raise it, and a
// fact GitHub can prove overrules both (internal/linha). Re-reading is safe
// because every Load rebuilds every spec from scratch.
//
// The reason lands in the body because the body is what the detail pane shows,
// and a verdict nobody can read is the same as no verdict: ceo-bora#321 asks,
// literally, that a merged pull request rendering in Done while the store says
// Ready To Review say so where a reader looks.
func (b *SourceBackend) resolveColumns(specs []*feature.Spec) (map[string]linha.Verdict, []error) {
	if b.line == nil {
		return nil, nil
	}
	var problems []error
	stored := map[string]string{}
	if b.line.Store != nil {
		columns, err := b.line.Store.Columns()
		if err != nil {
			// An unreadable store costs the overrides, not the board:
			// every fact is still measured and every card still draws.
			problems = append(problems, fmt.Errorf(
				"stored columns unread, so only measured facts place a card: %w", err))
		} else {
			stored = columns
		}
	}
	now := b.line.clock()
	verdicts := make(map[string]linha.Verdict, len(specs))
	for _, spec := range specs {
		repo := b.line.repoOf(spec)
		var (
			timer time.Duration
			set   bool
		)
		if b.line.Timer != nil {
			timer, set = b.line.Timer(repo)
		}
		verdict := linha.Resolve(linha.Input{
			Policy:   b.line.Policy,
			Labels:   spec.Labels,
			Source:   spec.Status,
			Stored:   feature.Status(stored[spec.ID]),
			Repo:     repo,
			Timer:    timer,
			TimerSet: set,
			Now:      now,
		})
		spec.Status = verdict.Column
		if verdict.Reason != "" {
			spec.Body = appendColumnReason(spec.Body, verdict)
		}
		verdicts[spec.ID] = verdict
	}
	return verdicts, problems
}

// appendColumnReason writes the line's own sentence into the card body.
func appendColumnReason(body string, verdict linha.Verdict) string {
	sentence := fmt.Sprintf("Coluna: %s — %s", verdict.Column, verdict.Reason)
	if body = strings.TrimRight(body, "\n"); body == "" {
		return sentence
	}
	return body + "\n\n" + sentence
}

// Move records a card's column in hvb's own store, or refuses and names who
// owns the answer instead.
//
// Five refusals, and each one is a different mistake:
//
//   - no line at all: every column comes from a file with its own author,
//     which is the board `hvb queue` has always been;
//   - a column the line does not draw: the answer would be unreachable;
//   - a card the last Load did not produce: there is no column to move it
//     FROM, so the transition check has nothing to check and the entry would
//     be a column for an id no board ever showed;
//   - a column a fact decides: the next Load would overrule the write, so
//     storing it is a lie with a timestamp on it;
//   - a transition the line does not declare: the picker greys those out, but
//     H/L and a direct call reach Move without asking the picker.
//
// Nothing here writes to GitHub. That is the point of decision C: a column
// GitHub has no field for is remembered by hvb, and a column it does have a
// field for is read from GitHub and never written.
func (b *SourceBackend) Move(_ context.Context, id string, target feature.Status, _ string) error {
	if b.line == nil {
		return b.refuse(fmt.Sprintf("move %s to %s", id, target), id)
	}
	if !b.workflow.Has(target) {
		return fmt.Errorf("the board cannot move %s to %s: the line does not draw that column (it draws: %s)",
			id, target, statusList(b.workflow.Columns()))
	}
	if label, ok := b.line.Policy.When[target]; ok {
		return fmt.Errorf("the board cannot move %s to %s: %s decides that column and only the forge sets it — the board stays read-only for GitHub (%w)",
			id, target, label, ErrReadOnly)
	}
	b.mu.Lock()
	verdict, known := b.verdicts[id]
	number := b.numbers[id]
	repo := b.repos[id]
	b.mu.Unlock()
	if !known {
		// The old code let this through: with no verdict there was no
		// column to transition from, so the two checks below were both
		// skipped and the store recorded a move the line had never
		// allowed, for a card that may not exist at all.
		return fmt.Errorf("the board cannot move %s to %s: no card with that id was on the last refresh, so the line has no column to move it from (press r to refresh, and check the id)",
			id, target)
	}
	switch {
	case verdict.Pinned:
		return fmt.Errorf("the board cannot move %s to %s: a measured fact holds it in %s (%s), and the next refresh would overrule the move",
			id, target, verdict.Column, verdict.Reason)
	case !b.workflow.CanTransition(verdict.Column, target):
		return fmt.Errorf("the line does not allow %s → %s (from %s you may move to: %s)",
			verdict.Column, target, verdict.Column, statusList(b.workflow.Next(verdict.Column)))
	}
	if b.line.Store == nil {
		return fmt.Errorf("the board cannot move %s to %s: this board has no column store to remember it in (%w)",
			id, target, ErrReadOnly)
	}
	return b.line.Store.Set(colunas.Entry{
		ID: id,
		// The card's OWN repository, not the issue queue's: a board
		// reading pull requests from one repository and issues from
		// another would otherwise file every stored pull request under
		// the issue repo, and `hvb state show` joins on that field.
		Repo:   repo,
		Issue:  number,
		Column: string(target),
		SetBy:  b.owner,
	})
}

func (b *SourceBackend) Create(_ context.Context, title string, _ []string, _ string) (string, error) {
	return "", fmt.Errorf("the board cannot create %q: it reads %s, and each of those files has its own author (%w)",
		title, b.sourceNames(), ErrReadOnly)
}

func (b *SourceBackend) SetField(_ context.Context, id, key, value string) error {
	return b.refuse(fmt.Sprintf("set %s=%q on %s", key, value, id), id)
}

// OwnsIssueDispatch reports whether this card goes to the line's own
// despatcher rather than to the run store's.
//
// "Um despachante por card" (ceo-bora#321): a GitHub issue is dispatched by
// `usina agente dispatch`, which cuts the worktree, opens the bora workspace
// and mints the scoped token for it. dispatch.Dispatcher keeps everything
// else, and that narrowing is deliberate rather than literal-minded. The
// issue's acceptance criterion names issue cards only, and a pull-request or
// FIOS card dispatched through --charters/--work-root is behaviour that works
// today and that the issue never asked to delete — so the rule is enforced
// where it was written, one card kind wide.
func (b *SourceBackend) OwnsIssueDispatch(spec *feature.Spec) bool {
	return b.line != nil && b.line.Issues != nil && spec != nil && spec.HasLabel(LabelSourceIssue)
}

// DispatchIssue hands an issue card to the line's despatcher and returns the
// sentence the board shows.
func (b *SourceBackend) DispatchIssue(ctx context.Context, spec *feature.Spec) (string, error) {
	if !b.OwnsIssueDispatch(spec) {
		return "", fmt.Errorf("the board cannot dispatch %s through the usina: this board was opened without --dispatch-repo, so no repository names the checkout the usina would cut a worktree from",
			specID(spec))
	}
	return b.line.Issues.DispatchIssue(ctx, spec)
}

// Dispatch launches an agent on a card when this board was given a repository
// to work in and charters to pick a role from, and refuses otherwise.
//
// The refusal is not mere absence of wiring: without --work-root there is no
// directory to run in, and dispatch would claim the feature through vb, which
// does not know these ids. Naming the source's own next step is more useful
// than a run that dies on a lock error, so that is what the board does.
//
// An issue card is refused outright, whatever the wiring: the run store's
// despatcher claims through vb, and vb has never heard of an issue number.
// Checking here rather than only in the key handler is what makes the rule
// hold for every caller — offerDispatch reaches Dispatch without going through
// the picker.
func (b *SourceBackend) Dispatch(ctx context.Context, spec *feature.Spec, role, kind string, force bool) (*runs.Run, error) {
	if err := b.refuseRunDispatch(spec); err != nil {
		return nil, err
	}
	if b.dispatcher == nil {
		return nil, b.refuseDispatch(spec, role)
	}
	return b.dispatcher.Start(ctx, dispatch.Request{Spec: spec, Role: role, Kind: kind, Force: force})
}

func (b *SourceBackend) DispatchWith(ctx context.Context, spec *feature.Spec, role, kind string, worktree, pr *bool) (*runs.Run, error) {
	if err := b.refuseRunDispatch(spec); err != nil {
		return nil, err
	}
	if b.dispatcher == nil {
		return nil, b.refuseDispatch(spec, role)
	}
	return b.dispatcher.Start(ctx, dispatch.Request{
		Spec: spec, Role: role, Kind: kind, Worktree: worktree, PR: pr,
	})
}

// refuseRunDispatch is the one-despatcher-per-card rule, spelled once so both
// dispatch entry points cannot drift apart.
func (b *SourceBackend) refuseRunDispatch(spec *feature.Spec) error {
	if !b.OwnsIssueDispatch(spec) {
		return nil
	}
	return fmt.Errorf("%s is a GitHub issue: it dispatches through `usina agente dispatch`, never through the run store's despatcher (press d on the card)",
		specID(spec))
}

// specID is a card's id, or a stand-in when there is no card at all, so a
// refusal never reads "cannot dispatch : …".
func specID(spec *feature.Spec) string {
	if spec == nil {
		return "no card"
	}
	return spec.ID
}

// Cancel and Focus follow the runs, not the cards: a run this board dispatched
// is a pane this board created, so it is allowed to close it or bring it into
// view. Neither touches the card's source, which is why they are not refusals.
func (b *SourceBackend) Cancel(ctx context.Context, runID string) error {
	if b.dispatcher == nil {
		return b.noRuns(runID)
	}
	_, err := b.dispatcher.Cancel(ctx, runID, "cancelled from the board")
	return err
}

func (b *SourceBackend) Focus(ctx context.Context, runID string) error {
	if b.dispatcher == nil {
		return b.noRuns(runID)
	}
	return b.dispatcher.Focus(ctx, runID)
}

// Roles is the charters a dispatch can choose from: the ones the dispatcher was
// loaded with, and none at all on a board that cannot dispatch. openMovePicker
// tolerates empty; openDispatchPicker asks DispatchUnavailable instead of
// assuming missing charters.
func (b *SourceBackend) Roles() []roles.Role {
	if b.dispatcher == nil {
		return nil
	}
	return b.dispatcher.Roles
}

// DispatchUnavailable is why pressing d does nothing on a read-only board, in
// the board's own words: the card is a pull request, an issue, a FIOS.md thread
// or a gate box, and it names where to act instead. Without this the picker
// blamed absent charters in .virtualboard/agents — a directory this board never
// reads, and creating it would not have made dispatch work either. It is only
// ever asked when Roles() is empty, which is exactly the no-dispatcher case.
func (b *SourceBackend) DispatchUnavailable(spec *feature.Spec) error {
	return b.refuseDispatch(spec, "")
}

func (b *SourceBackend) Config() *config.Config { return b.config }

// Workflow is the line this board draws, which is the one it was built with.
func (b *SourceBackend) Workflow() *feature.Workflow { return b.workflow }

func (b *SourceBackend) Owner() string { return b.owner }

func (b *SourceBackend) ProjectName() string { return b.project }

// refuse builds the refusal for a card, naming the file that owns it.
func (b *SourceBackend) refuse(action, id string) error {
	return fmt.Errorf("the board cannot %s: %s (%w)", action, b.origin(id), ErrReadOnly)
}

// refuseDispatch is the refusal for a board opened without a dispatch half. It
// names the flags that turn dispatch on as well as the place the card lives:
// the user pressed a key that can work here, and the reason it did not is a
// missing pair of flags, not the card.
func (b *SourceBackend) refuseDispatch(spec *feature.Spec, role string) error {
	id := ""
	if spec != nil {
		id = spec.ID
	}
	if role == "" {
		role = "an agent"
	}
	return fmt.Errorf("the board cannot dispatch %s onto %s: it was opened without --charters and --work-root, so it has no role to pick and no repository to work in; %s (%w)",
		role, id, b.origin(id), ErrReadOnly)
}

func (b *SourceBackend) noRuns(runID string) error {
	return fmt.Errorf("%s is not a run this board owns: %s reads %s and dispatches nothing, so it records no runs",
		runID, b.project, b.sourceNames())
}

// origin is the remembered sentence for a card, or a usable fallback for an id
// the board has not loaded.
func (b *SourceBackend) origin(id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sentence, ok := b.origins[id]; ok {
		return sentence
	}
	return fmt.Sprintf("%s comes from a read-only source; edit it where it lives", id)
}

// sourceNames describes what this board is reading, for messages that cannot
// name a single card.
func (b *SourceBackend) sourceNames() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.kinds) == 0 {
		return "read-only sources"
	}
	return strings.Join(b.kinds, ", ")
}

// sourceKind names the source a card came from, from its source: label.
//
// The pull-request source is tested first, and that order is load-bearing. It
// is the only source that carries labels it did not mint — a repository's own
// labels ride along verbatim — so a pull request is the only card that can
// arrive claiming to be something else. `hvb:source:pr` therefore wins: the
// sources that mint no foreign labels can never produce it, so its presence
// says which source built the card regardless of what else is on it.
func sourceKind(spec *feature.Spec) string {
	switch {
	case spec.HasLabel(LabelSourcePR):
		return "GitHub pull requests"
	case spec.HasLabel(LabelSourceFios):
		return "FIOS.md"
	case spec.HasLabel(LabelSourceGates):
		return "gates/"
	case spec.HasLabel(LabelSourceIssue):
		return "GitHub issues"
	default:
		return ""
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// issueNumber is the issue a card IS, read off its own label.
//
// Only an issue card answers. internal/ghboard mints the same label prefix for
// the issue a pull request CLOSES, so reading it from a pull request would file
// that card under an issue it is not — and the store's Issue field is what a
// caller of `hvb state show` joins on.
func issueNumber(spec *feature.Spec) int {
	if spec == nil || !spec.HasLabel(LabelSourceIssue) {
		return 0
	}
	number, err := strconv.Atoi(strings.TrimSpace(labelValue(spec, labelIssuePrefix)))
	if err != nil {
		return 0
	}
	return number
}

// describeOrigin turns a card into the sentence a refusal shows. It reads only
// the labels and the path the contract guarantees, so it works for any source.
//
// Ordered like sourceKind, and for the same reason: a pull request is the only
// card that carries labels it did not mint, so a forged `hvb:source:fios` on
// one must not send the user to FIOS.md looking for a pull request.
func describeOrigin(spec *feature.Spec) string {
	file := ""
	if spec.Path != "" {
		file = filepath.Base(spec.Path)
	}
	switch {
	case spec.HasLabel(LabelSourcePR):
		// The number comes off the pr: label, never off the id: ids are
		// opaque strings and a source may change how it mints them.
		if number := labelValue(spec, labelPRPrefix); number != "" {
			return fmt.Sprintf("%s is a pull request; act on it on GitHub (gh pr view %s)", spec.ID, number)
		}
		return fmt.Sprintf("%s is a pull request; act on it on GitHub", spec.ID)
	case spec.HasLabel(LabelSourceIssue):
		// An issue's column is decided by usina and kit.py, not by this
		// board, so the refusal names both places rather than only the
		// issue: moving the card here would be a lie either way.
		if number := labelValue(spec, labelIssuePrefix); number != "" {
			return fmt.Sprintf(
				"%s is a GitHub issue; act on it on GitHub (gh issue view %s) and let usina rank it",
				spec.ID, number)
		}
		return fmt.Sprintf("%s is a GitHub issue; act on it on GitHub and let usina rank it", spec.ID)
	case spec.HasLabel(LabelSourceFios):
		if file == "" {
			file = "FIOS.md"
		}
		return fmt.Sprintf("%s lives in %s; edit it there or run bin/fios-check", spec.ID, file)
	case spec.HasLabel(LabelSourceGates):
		if gate := labelValue(spec, labelGatePrefix); gate != "" {
			file = gate
		}
		if file == "" {
			file = "the gate ledger"
		}
		return fmt.Sprintf("%s lives in %s; edit it there and keep the gate open in the shape gates/CONVENCAO.md describes", spec.ID, file)
	case file != "":
		return fmt.Sprintf("%s lives in %s; edit it there", spec.ID, file)
	default:
		return fmt.Sprintf("%s comes from a read-only source; edit it where it lives", spec.ID)
	}
}

// labelValue returns the remainder of the first label with the given prefix.
func labelValue(spec *feature.Spec, prefix string) string {
	for _, label := range spec.Labels {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimPrefix(label, prefix)
		}
	}
	return ""
}

// sourceMeta is the chips a source-backed card carries besides its labels:
// whether the item is still a draft and whether its author is outside the
// repository. The linked issue and the age live in the card HEADER now
// (renderCard), where the reader looks for who and what before reading the
// title. The old version minted "Issue #N" here from the issue prefix
// regardless of kind, so an issue card announced its own number as a link to
// itself — cardLinkedIssuesLabel owns that distinction.
//
// A card with no source: label gets nothing.
func (m *Model) sourceMeta(card *Card) []string {
	spec := card.Spec
	if sourceKind(spec) == "" {
		return nil
	}
	var out []string
	if spec.HasLabel(LabelDraft) {
		// A draft is in review but nobody is asking for one yet.
		out = append(out, paint(m.palette.Dim, "Draft"))
	}
	if spec.HasLabel(LabelExternal) {
		out = append(out, paint(m.palette.Warn, "External"))
	}
	return out
}

// specAge is how long ago the card last changed, from the RFC3339 timestamps
// the sources write into the frontmatter. A card without a parseable timestamp
// shows no age rather than a zero.
func specAge(spec *feature.Spec, now time.Time) (time.Duration, bool) {
	for _, stamp := range []string{spec.Updated, spec.Created} {
		stamp = strings.TrimSpace(stamp)
		if stamp == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			parsed, err := time.Parse(layout, stamp)
			if err != nil {
				continue
			}
			age := now.Sub(parsed)
			if age < 0 {
				age = 0
			}
			return age, true
		}
	}
	return 0, false
}

// cardID is the id as a card shows it.
//
// A pull request reads as the number GitHub calls it by, taken from the pr:
// label rather than from the id, because an id is an opaque string: a source
// may mint it from a content hash so that a stored run keeps pointing at the
// card it was dispatched for. The only id text this touches is VirtualBoard's
// own FTR- prefix, which the board has always dropped — it is noise next to
// four more of them in the same column.
func cardID(spec *feature.Spec) string {
	if number := labelValue(spec, labelPRPrefix); number != "" {
		return "#" + number
	}
	return strings.TrimPrefix(spec.ID, "FTR-")
}
