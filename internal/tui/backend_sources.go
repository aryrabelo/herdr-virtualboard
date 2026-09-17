package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
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

// StatusCanceled is a presentation-only sixth column for work that ended
// without landing: a pull request closed unmerged.
//
// It is deliberately not a feature.Status constant. VirtualBoard's lifecycle
// has exactly five states (internal/feature/status.go:11-21) and `done` is
// terminal by rule (status.go:68-70), so a sixth domain state would mean a new
// transition table in a package vb also owns. Instead the sources report such
// a card as feature.Done plus LabelCanceled, and the board splits the two
// apart when it builds its columns — feature.Status is a string type, so the
// TUI can mint a value that only it knows about. feature.Status("canceled")
// answers Valid() false, which is exactly right: the move picker
// (update.go:256-268) offers only real statuses, so no user can try to
// transition into or out of this column.
const StatusCanceled feature.Status = "canceled"

// boardColumns is feature.Statuses plus the derived column, built once so a
// frame costs no allocation for it.
var boardColumns = append(append(make([]feature.Status, 0, len(feature.Statuses)+1),
	feature.Statuses...), StatusCanceled)

// boardStatus is the column a spec belongs in, which is its status everywhere
// except for the cancelled tail of `done`.
//
// Both conditions are required, not just the label. `state:canceled` means one
// thing — a pull request that closed without merging — so only a card from the
// pull-request source can land in that column. Any other source reporting the
// label gets Done, which is where a card whose label the board does not
// recognise belongs.
func boardStatus(spec *feature.Spec) feature.Status {
	if spec.Status == feature.Done && spec.HasLabel(LabelSourcePR) && spec.HasLabel(LabelCanceled) {
		return StatusCanceled
	}
	return spec.Status
}

// columnOrder is the board's columns, left to right. The derived column appears
// only when something is in it: a board over a VirtualBoard workspace, where
// no card is ever cancelled, looks exactly as it did before.
func (m *Model) columnOrder() []feature.Status {
	if len(m.columns[StatusCanceled]) == 0 {
		return feature.Statuses
	}
	return boardColumns
}

// ErrReadOnly marks a mutation the board refused because the card's source owns
// the file. The message names the owning file and the tool allowed to edit it;
// a silent no-op would leave the user pressing a key that appears to work.
var ErrReadOnly = errors.New("read-only source")

// SourceBackend is a Backend over read-only sources: the owner's own queue
// (FIOS.md, gates/) and the week's pull requests, rather than VirtualBoard spec
// markdown. It reads, it draws, and it refuses every write with an explanation.
type SourceBackend struct {
	sources []Source
	config  *config.Config
	owner   string
	project string

	// origins remembers, per card, the sentence that says where it lives, so
	// a refusal can name the file without going back to the sources. kinds is
	// the same thing for the board as a whole, for messages that cannot name
	// a single card.
	mu      sync.Mutex
	origins map[string]string
	kinds   []string
}

// NewSourceBackend composes already-built sources into a board. Sources are
// injected rather than constructed from paths so the board is testable without
// a vault on disk and without the gh binary.
func NewSourceBackend(project string, cfg *config.Config, owner string, sources ...Source) *SourceBackend {
	if cfg == nil {
		defaults := config.Default()
		cfg = &defaults
	}
	if owner == "" {
		owner = cfg.ResolveOwner()
	}
	if project == "" {
		project = "queue"
	}
	return &SourceBackend{
		sources: sources, config: cfg, owner: owner, project: project,
		origins: map[string]string{},
	}
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

	origins := make(map[string]string, len(specs))
	var kinds []string
	for _, spec := range specs {
		origins[spec.ID] = describeOrigin(spec)
		if kind := sourceKind(spec); kind != "" && !contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	b.mu.Lock()
	b.origins, b.kinds = origins, kinds
	b.mu.Unlock()

	// No run store: nothing on this board was dispatched from it.
	return specs, nil, problems
}

func (b *SourceBackend) Move(_ context.Context, id string, target feature.Status, _ string) error {
	return b.refuse(fmt.Sprintf("move %s to %s", id, target), id)
}

func (b *SourceBackend) Create(_ context.Context, title string, _ []string, _ string) (string, error) {
	return "", fmt.Errorf("the board cannot create %q: it reads %s, and each of those files has its own author (%w)",
		title, b.sourceNames(), ErrReadOnly)
}

func (b *SourceBackend) SetField(_ context.Context, id, key, value string) error {
	return b.refuse(fmt.Sprintf("set %s=%q on %s", key, value, id), id)
}

// Dispatch refuses, and says why.
//
// Dispatching is not merely unwired here — it cannot work for these cards as
// the dispatcher stands. internal/dispatch/dispatch.go:115 claims the feature
// before anything else, and claim (dispatch.go:363-377) calls vb.Acquire on the
// feature id with a lock TTL that defaults to 60 minutes
// (internal/config/config.go:175); vb has never heard of FIO-3 or PR-179, so
// the dispatch aborts before a pane exists. Past the claim, launch reads
// d.Workspace.Rel(spec.Path) (dispatch.go:242) and completion settles a run
// with vb move (internal/dispatch/complete.go:147-171) — both of which need a
// VirtualBoard workspace that owns the id.
//
// Refusing with the source's own next step is more useful than a run that dies
// on a lock error, so that is what the board does.
func (b *SourceBackend) Dispatch(_ context.Context, spec *feature.Spec, role, _ string, _ bool) (*runs.Run, error) {
	return nil, b.refuseDispatch(spec, role)
}

func (b *SourceBackend) DispatchWith(_ context.Context, spec *feature.Spec, role, _ string, _, _ *bool) (*runs.Run, error) {
	return nil, b.refuseDispatch(spec, role)
}

func (b *SourceBackend) Cancel(_ context.Context, runID string) error { return b.noRuns(runID) }

func (b *SourceBackend) Focus(_ context.Context, runID string) error { return b.noRuns(runID) }

// Roles is empty: role charters live in a VirtualBoard workspace, and this
// board does not have one. The dispatch picker degrades to "no roles", which is
// the truth, and openMovePicker/openDispatchPicker tolerate it.
func (b *SourceBackend) Roles() []roles.Role { return nil }

func (b *SourceBackend) Config() *config.Config { return b.config }

func (b *SourceBackend) Owner() string { return b.owner }

func (b *SourceBackend) ProjectName() string { return b.project }

// refuse builds the refusal for a card, naming the file that owns it.
func (b *SourceBackend) refuse(action, id string) error {
	return fmt.Errorf("the board cannot %s: %s (%w)", action, b.origin(id), ErrReadOnly)
}

func (b *SourceBackend) refuseDispatch(spec *feature.Spec, role string) error {
	id := ""
	if spec != nil {
		id = spec.ID
	}
	if role == "" {
		role = "an agent"
	}
	return fmt.Errorf("the board cannot dispatch %s onto %s: dispatch claims the feature through vb, which does not know this id; %s (%w)",
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

// sourceMeta is the extra card facts the read-only sources carry: the linked
// issue, whether the item is still a draft, whether its author is outside the
// repository, and how old it is. Everything comes off the labels and the
// frontmatter the contract fills, so renderCard stays one concern and no source
// has to know how a card is drawn.
//
// A card with no source: label gets nothing. A VirtualBoard spec also carries
// created/updated dates, so without this gate every card on the spec-markdown
// board grew an age it never used to show.
func (m *Model) sourceMeta(card *Card) []string {
	spec := card.Spec
	if sourceKind(spec) == "" {
		return nil
	}
	var out []string
	if issue := labelValue(spec, labelIssuePrefix); issue != "" {
		out = append(out, paint(m.palette.Accent, "Issue #"+issue))
	}
	if spec.HasLabel(LabelDraft) {
		// A draft is in review but nobody is asking for one yet.
		out = append(out, paint(m.palette.Dim, "Draft"))
	}
	if spec.HasLabel(LabelExternal) {
		out = append(out, paint(m.palette.Warn, "External"))
	}
	if age, ok := specAge(spec, time.Now()); ok {
		out = append(out, paint(m.palette.Dim, shortDuration(age)))
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
