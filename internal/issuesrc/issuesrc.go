// Package issuesrc turns the owner's issue queue into board cards.
//
// It is the seam between two measured packages and owns no policy of its own:
// internal/usinasrc reads the issues (usina first, gh as a declared
// degradation) and internal/fila decides the column and reads the frontier.
// This package only composes them into feature.Spec values and reports, as
// problems rather than as silence, every degradation either one declared.
//
// The board never writes here. An issue lives on GitHub and its queue position
// is decided by usina and kit.py, so every mutation is refused with the place
// that owns it.
package issuesrc

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/fila"
	"github.com/virtualboard/herdr-virtualboard/internal/usinasrc"
)

// LabelPrefix is the reserved namespace, spelled here for the same reason the
// other sources spell it: a repository can own a label literally named
// `source:usina`, and a board deriving anything from an unprefixed name would
// be taking dictation from content anyone can edit in the GitHub UI. Incoming
// labels that spell the prefix are dropped.
const LabelPrefix = "hvb:"

// The labels this source mints. LabelSourceIssue says "this card is a GitHub
// issue of the owner's queue"; the two origin labels say which binary answered,
// so the degradation is visible on the card itself and not only in a log line
// nobody reads.
const (
	LabelSourceIssue = LabelPrefix + "source:issue"
	LabelOriginUsina = LabelPrefix + "origin:usina"
	LabelOriginGH    = LabelPrefix + "origin:gh"

	// LabelIssuePrefix carries the issue number, and is the same spelling
	// internal/ghboard uses for the issue a pull request closes: a card and
	// the pull request that lands it therefore agree on one key.
	LabelIssuePrefix = LabelPrefix + "issue:"
	// LabelRankPrefix carries the frontier's rank, which exists only for
	// the issues kit.py ranked.
	LabelRankPrefix = LabelPrefix + "rank:"
)

// Runner is the single process-runner both underlying packages need. It is
// declared unnamed-compatible with usinasrc.Runner and fila.Runner on purpose:
// one injected function serves usina, gh, and kit.py, so a test drives the
// whole source with one fixture reader and never executes anything.
type Runner func(name string, args ...string) ([]byte, error)

// Source reads label's issues of ceoRepo and, when kitPath, slug and
// frontierRepo are given, ranks them with kit.py's frontier.
type Source struct {
	run     Runner
	ceoRepo string
	label   string
	kitPath string
	slug    string
	// frontierRepo is the repository kit.py's issue NUMBERS belong to, and
	// it exists because they are not necessarily the queue's.
	//
	// Measured 2026-09-17: `kit.py fronteira bugtoprompt` ranks issue 31,
	// which is aryrabelo/bugtoprompt#31 ("CI red: deploy on main"), an
	// execution-repository issue — while aryrabelo/ceo-bora#31 is a closed,
	// unrelated issue about channel identity. kit.py's own answer names
	// neither repository (its keys are verbo, slug, pronto, bloqueado), so
	// joining on a bare number would have ranked, or BLOCKED, whichever
	// card happened to share the integer. In that measurement the wrong
	// card was closed, so the defect was invisible: `closed` wins the
	// column before any frontier, and the board looked right by luck.
	frontierRepo string
	limit        int
}

// DefaultLimit is how many issues one read asks for. The owner's largest live
// project measured 32 issues (2026-09-17, project:bugtoprompt), so this is
// generous rather than tight; a queue that hits it is a queue nobody is
// reading card by card anyway.
const DefaultLimit = 200

// New builds the source. run is injected so the board is testable without
// usina, gh or python3 on PATH.
//
// kitPath, slug and frontierRepo are optional as a set: without them the board
// still draws every card, only unranked, which is a narrower board rather than
// a wrong one.
func New(run Runner, ceoRepo, label, kitPath, slug, frontierRepo string, limit int) *Source {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Source{
		run:          run,
		ceoRepo:      strings.TrimSpace(ceoRepo),
		label:        strings.TrimSpace(label),
		kitPath:      strings.TrimSpace(kitPath),
		slug:         strings.TrimSpace(slug),
		frontierRepo: strings.TrimSpace(frontierRepo),
		limit:        limit,
	}
}

// Load reads the queue and returns one card per issue.
//
// Every degradation is returned as an error alongside the cards, never
// swallowed: the board shows the problems it was told about, and a card read
// through gh because usina was silent is still a card. A hard failure — both
// binaries dead, or a mis-wired call — returns no cards and says which
// binaries it asked.
func (s *Source) Load(ctx context.Context) ([]*feature.Spec, []error) {
	if s == nil || s.run == nil {
		return nil, []error{fmt.Errorf("issuesrc: no runner, so no queue can be read")}
	}
	if err := ctx.Err(); err != nil {
		return nil, []error{err}
	}

	queue, err := usinasrc.Load(usinasrc.Runner(s.run), s.ceoRepo, s.label, s.limit)
	if err != nil {
		return nil, []error{err}
	}

	var errs []error
	if queue.Origin.Reason != "" {
		errs = append(errs, fmt.Errorf("issue queue served by %s: %s",
			originName(queue.Origin.Source), queue.Origin.Reason))
	}
	if queue.RouteRefusal != "" {
		// The route's own refusal is data, not a failure: it says which
		// part of the queue usina declined to route, and the cards it did
		// route are still real.
		errs = append(errs, fmt.Errorf("usina rota recusou: %s", queue.RouteRefusal))
	}

	frontier := fila.Frontier{Priority: map[int]fila.Priority{}, Blocked: map[int]bool{}}
	switch {
	case s.kitPath == "" || s.slug == "":
		// No frontier asked for: an unranked board, and nothing to say.
	case s.frontierRepo == "":
		errs = append(errs, fmt.Errorf(
			"frontier not joined, cards are unranked: nothing named the repository kit.py's issue numbers belong to, and joining them onto %s by number alone would rank or block whichever card shares the integer",
			queue.Repo))
	case !strings.EqualFold(s.frontierRepo, queue.Repo):
		errs = append(errs, fmt.Errorf(
			"frontier not joined, cards are unranked: kit.py ranks %s and this queue is %s, so their issue numbers name different issues",
			s.frontierRepo, queue.Repo))
	default:
		if err := ctx.Err(); err != nil {
			return nil, []error{err}
		}
		measured, err := fila.LoadFrontier(fila.Runner(s.run), s.kitPath, s.slug)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("frontier unread, cards are unranked: %w", err))
		default:
			frontier = measured
			if measured.Reason != "" {
				errs = append(errs, fmt.Errorf("frontier unread, cards are unranked: %s",
					measured.Reason))
			}
		}
	}

	specs := make([]*feature.Spec, 0, len(queue.Cards))
	for _, card := range queue.Cards {
		specs = append(specs, spec(card, queue, frontier))
	}
	sortSpecs(specs, frontier)
	return specs, errs
}

// spec converts one issue to a card.
func spec(card usinasrc.Card, queue usinasrc.Queue, frontier fila.Frontier) *feature.Spec {
	owner := ""
	if len(card.Assignees) > 0 {
		owner = card.Assignees[0]
	}
	priority, ranked := frontier.Priority[card.Number]

	// The column is fila's decision, never this package's. fila.Column and
	// feature.Status spell the same five lanes, and a test pins that.
	column := fila.ColumnFor(card.Closed, card.Labels, owner != "", frontier.Blocked[card.Number])

	labels := make([]string, 0, len(card.Labels)+4)
	labels = append(labels, LabelSourceIssue)
	if origin := originLabel(queue.Origin.Source); origin != "" {
		labels = append(labels, origin)
	}
	labels = append(labels, LabelIssuePrefix+strconv.Itoa(card.Number))
	if ranked {
		labels = append(labels, LabelRankPrefix+strconv.Itoa(priority.Rank))
	}
	for _, label := range card.Labels {
		// Reserved: a repository label spelling the namespace would let
		// content forge a column or a badge.
		if label = strings.TrimSpace(label); label != "" && !strings.HasPrefix(label, LabelPrefix) {
			labels = append(labels, label)
		}
	}

	return &feature.Spec{
		Frontmatter: feature.Frontmatter{
			ID:     ID(queue.Repo, card.Number),
			Title:  strings.TrimSpace(card.Title),
			Status: feature.Status(string(column)),
			Owner:  owner,
			// Priority is deliberately left empty. vb's priority is a
			// declared word (critical/high/…) and the frontier's rank is
			// a measured position; writing the rank here would make the
			// board sort by a value that looks declared and is not. The
			// rank travels as a label and as the sort order instead.
			Labels: labels,
		},
		// Path is where the card lives, and an issue does not live on disk.
		// The URL is the honest answer and it is what the refusal names.
		Path: card.URL,
		Body: body(card, priority, ranked),
	}
}

// ID is the card's identifier: the repository that owns the issue plus its
// number, so two queues read side by side cannot collide.
func ID(repo string, number int) string {
	if _, name, ok := splitRepo(repo); ok {
		return name + "#" + strconv.Itoa(number)
	}
	return "issue#" + strconv.Itoa(number)
}

// body is what the card shows when opened: the URL, and the frontier's own
// sentence about why this issue is next when it said one.
func body(card usinasrc.Card, priority fila.Priority, ranked bool) string {
	lines := make([]string, 0, 3)
	if card.URL != "" {
		lines = append(lines, card.URL)
	}
	if ranked && strings.TrimSpace(priority.Why) != "" {
		lines = append(lines, fmt.Sprintf("fronteira #%d: %s", priority.Rank, strings.TrimSpace(priority.Why)))
	}
	return strings.Join(lines, "\n")
}

// sortSpecs puts ranked work first, in the frontier's order, and everything
// else after it by descending issue number — newest first, which is the order
// the owner reads an unranked pile in.
func sortSpecs(specs []*feature.Spec, frontier fila.Frontier) {
	rank := func(spec *feature.Spec) (int, bool) {
		number := issueNumber(spec)
		priority, ok := frontier.Priority[number]
		return priority.Rank, ok
	}
	sort.SliceStable(specs, func(i, j int) bool {
		leftRank, leftRanked := rank(specs[i])
		rightRank, rightRanked := rank(specs[j])
		if leftRanked != rightRanked {
			return leftRanked
		}
		if leftRanked && leftRank != rightRank {
			return leftRank < rightRank
		}
		return issueNumber(specs[i]) > issueNumber(specs[j])
	})
}

// issueNumber reads the number off the card's own label rather than off its id:
// the id is an opaque string this package mints and may change, the label is
// the contract every source shares.
func issueNumber(spec *feature.Spec) int {
	for _, label := range spec.Labels {
		if !strings.HasPrefix(label, LabelIssuePrefix) {
			continue
		}
		if number, err := strconv.Atoi(strings.TrimPrefix(label, LabelIssuePrefix)); err == nil {
			return number
		}
	}
	return 0
}

// ExecRunner is the production Runner: usinasrc's, reused verbatim.
//
// One runner for all three binaries is not a shortcut — it is the reason a
// single injected function can drive usina, gh and python3 in a test. The
// implementation is generic (LookPath, no stdin, a timeout, stderr folded into
// the error), so nothing about it is usina-specific, and duplicating it here
// would buy a second thing to keep in step for nothing.
//
// It runs the children in the current working directory, which is usually the
// wrong one: see DirRunner.
func ExecRunner(name string, args ...string) ([]byte, error) {
	return usinasrc.ExecRunner(name, args...)
}

// DirRunner runs the children inside dir.
//
// This is not a convenience. Both tools resolve WHICH queue they are talking
// about from the working directory, and measured 2026-09-17 from outside the
// CEO repository they do not degrade gracefully — they refuse: usina answers
// "instancia da usina indeterminada: nenhum segmento" and kit.py answers "nao
// estou num repo git — rode dentro do repo do time". A board launched from a
// worktree would therefore read every card through the gh fallback and show no
// ranking at all, with two declared degradations explaining why: correct
// behaviour, and a uselessly narrow board.
//
// An empty dir is the current directory, so this is always safe to wrap.
func DirRunner(dir string) Runner {
	if strings.TrimSpace(dir) == "" {
		return ExecRunner
	}
	return func(name string, args ...string) ([]byte, error) {
		return usinasrc.ExecRunnerIn(dir, name, args...)
	}
}

// originLabel maps the binary that answered to the label saying so. An unknown
// value mints nothing: an invented label is worse than an absent one.
func originLabel(source string) string {
	switch source {
	case usinasrc.SourceUsina:
		return LabelOriginUsina
	case usinasrc.SourceGH:
		return LabelOriginGH
	default:
		return ""
	}
}

// originName names the binary for a human-readable problem line.
func originName(source string) string {
	if source == "" {
		return "nobody"
	}
	return source
}

func splitRepo(repo string) (owner, name string, ok bool) {
	repo = strings.TrimSpace(repo)
	owner, name, found := strings.Cut(repo, "/")
	if !found {
		return "", "", false
	}
	owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
	if owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}
