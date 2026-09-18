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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/fila"
	"github.com/virtualboard/herdr-virtualboard/internal/usinasrc"
	"gopkg.in/yaml.v3"
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

	// The state fact: exactly one of these is on every card. The line
	// policy reads it instead of reading the card's column, because the
	// column is fila's derivation and configuration can rename it, while
	// "the issue is closed" is a fact GitHub reported. The spelling is
	// internal/ghboard's `state:` namespace, so one vocabulary covers
	// issues and pull requests.
	LabelStateClosed = LabelPrefix + "state:closed"
	LabelStateOpen   = LabelPrefix + "state:open"
)

// Runner is the single process-runner both underlying packages need. It is
// declared unnamed-compatible with usinasrc.Runner and fila.Runner on purpose:
// one injected function serves usina, gh, and kit.py, so a test drives the
// whole source with one fixture reader and never executes anything.
type Runner func(name string, args ...string) ([]byte, error)

// Source reads label's issues of ceoRepo and, when kitPath and slug are
// given, ranks them with kit.py's frontier.
type Source struct {
	run     Runner
	ceoRepo string
	label   string
	kitPath string
	slug    string
	// frontierRepo is the human's declaration of the repository kit.py's
	// issue NUMBERS belong to. It is optional, and it is only ever a
	// cross-check: the repository is DERIVED from the project charter, and
	// a declaration the charter contradicts refuses the join instead of
	// winning it.
	//
	// The repository matters because kit.py's numbers are not necessarily
	// the queue's. Measured 2026-09-17: `kit.py fronteira bugtoprompt`
	// ranks issue 31, which is aryrabelo/bugtoprompt#31 ("CI red: deploy on
	// main"), an execution-repository issue — while aryrabelo/ceo-bora#31
	// is a closed, unrelated issue about channel identity. kit.py's own
	// answer names neither repository (its keys are verbo, slug, pronto,
	// bloqueado), so joining on a bare number would have ranked, or
	// BLOCKED, whichever card happened to share the integer. In that
	// measurement the wrong card was closed, so the defect was invisible:
	// `closed` wins the column before any frontier, and the board looked
	// right by luck.
	//
	// It used to be required, which made the board's correctness depend on
	// a flag a human types — and a human who types the wrong owner/name
	// gets a board that only says it did not join. The charter cannot be
	// mistyped here, because it is the same file kit.py itself reads.
	frontierRepo string
	limit        int
	// notes are problems the caller measured while wiring this source,
	// before any binary was asked anything. See Note.
	notes []error
}

// DefaultLimit is how many issues one read asks for. The owner's largest live
// project measured 32 issues (2026-09-17, project:bugtoprompt), so this is
// generous rather than tight; a queue that hits it is a queue nobody is
// reading card by card anyway.
const DefaultLimit = 200

// New builds the source. run is injected so the board is testable without
// usina, gh or python3 on PATH.
//
// kitPath and slug are optional as a pair: without them the board still draws
// every card, only unranked, which is a narrower board rather than a wrong
// one. With them the repository whose issue numbers kit.py ranks is derived
// from the project charter — measured 2026-09-17, `kit.py fronteira <slug>`
// resolves that repository by reading <CEORoot>/projetos/<slug>/charter.md and
// taking the `repo:` front-matter field (projetos/bugtoprompt/charter.md says
// `repo: aryrabelo/bugtoprompt`), so the charter is not a second source of
// truth but the same one.
//
// frontierRepo is therefore an optional override, kept only so a caller can
// assert what it believes: agreeing with the charter changes nothing, and
// disagreeing with it refuses the join and names both values.
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

// Note attaches a problem the caller measured while wiring this source, so it
// reaches the board through the one problem list the board already draws.
//
// The problems worth attaching are the ones no subprocess can discover for the
// board. WorkDir's refusal is the measured example: it is a property of a
// path, known before usina is asked anything, and it is the reason the very
// next line of Load will say "issue queue served by gh". A second, card-less
// source would report the same sentence in a second place; one queue keeps one
// problem list.
//
// A nil problem attaches nothing, so a caller never has to branch.
func (s *Source) Note(problem error) *Source {
	if s == nil || problem == nil {
		return s
	}
	s.notes = append(s.notes, problem)
	return s
}

// Load reads the queue and returns one card per issue.
//
// Every degradation is returned as an error alongside the cards, never
// swallowed: the board shows the problems it was told about, and a card read
// through gh because usina was silent is still a card. A hard failure — both
// binaries dead, or a mis-wired call — returns no cards and says which
// binaries it asked. Anything the caller measured while wiring the source (see
// Note) is reported first, because it explains what follows it.
func (s *Source) Load(ctx context.Context) ([]*feature.Spec, []error) {
	if s == nil || s.run == nil {
		return nil, []error{fmt.Errorf("issuesrc: no runner, so no queue can be read")}
	}
	if err := ctx.Err(); err != nil {
		return nil, []error{err}
	}

	// The notes were measured before anything ran and they explain the
	// degradations below them, so they come first — including when there
	// is nothing below them: a queue neither binary could answer is
	// exactly the case a note about the working directory explains.
	errs := append([]error(nil), s.notes...)

	queue, err := usinasrc.Load(usinasrc.Runner(s.run), s.ceoRepo, s.label, s.limit)
	if err != nil {
		return nil, append(errs, err)
	}

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
	default:
		if refusal := s.frontierRefusal(queue.Repo); refusal != nil {
			errs = append(errs, refusal)
			break
		}
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

// frontierRefusal answers whether kit.py's frontier may be joined onto
// queueRepo's cards, and returns the refusal to report when it may not.
//
// Nil means join. Every other answer is a sentence naming the values that
// disagree and the file that settled it, because the alternative — joining on
// a bare integer — silently moves whichever card shares the number.
func (s *Source) frontierRefusal(queueRepo string) error {
	charter := charterPath(s.kitPath, s.slug)
	ranked, err := charterRepo(charter)
	switch {
	case err != nil:
		// Absence is absence: no fallback to the CEO repository. kit.py
		// reads this same file to decide which repository's issues to
		// rank, so a charter that does not name one is a frontier nobody
		// can place, and the path is the only actionable part.
		return fmt.Errorf(
			"frontier not joined, cards are unranked: %s does not name the repository kit.py's issue numbers belong to (%v), and joining them onto %s by number alone would rank or block whichever card shares the integer",
			charter, err, queueRepo)
	case s.frontierRepo != "" && !strings.EqualFold(s.frontierRepo, ranked):
		// A declaration the charter contradicts is a wrong declaration,
		// and it has to read as one: the human typed an owner/name, the
		// file kit.py itself reads says another, and ranking by number
		// while that is unresolved would move real cards.
		return fmt.Errorf(
			"frontier not joined, cards are unranked: --kit-repo says %s but %s says kit.py ranks %s, so one of the two is wrong",
			s.frontierRepo, charter, ranked)
	case !strings.EqualFold(ranked, queueRepo):
		return fmt.Errorf(
			"frontier not joined, cards are unranked: kit.py ranks %s and this queue is %s, so their issue numbers name different issues",
			ranked, queueRepo)
	}
	return nil
}

// CEORoot is the CEO repository kitPath lives in.
//
// kit.py lives in bin/kit.py, so the repository is its grandparent. Two
// callers need that directory — the command, to run the children inside it
// (see DirRunner), and this package, to find the project charter — so the
// layout fact is spelled once here instead of drifting in two places.
//
// An empty kitPath yields an empty root rather than ".", which DirRunner reads
// as "the current directory": there is nothing to derive from, and an invented
// directory would send the children somewhere.
func CEORoot(kitPath string) string {
	kitPath = strings.TrimSpace(kitPath)
	if kitPath == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(kitPath))
}

// WorkDir resolves the directory the queue's children must run in, and the
// problem that directory already proves.
//
// Two independent facts used to travel in one flag. Asking for the frontier
// means naming kit.py, and kit.py's path happened to be the only thing a
// caller could derive the working directory from (CEORoot) — so a board that
// did not want the ranking got no usina either. Measured 2026-09-17 from
// ~/Sites/bora-team/bugtoprompt, with the CEO repository nowhere in the path,
// usina exits 2 with:
//
//	instância da usina indeterminada: nenhum segmento 'ceo-<nome>' no cwd
//	(<cwd>) — rode de dentro do checkout (ou worktree) de um CEO, ou aponte
//	USINA_CONFIG pro yml da instância; a usina nunca adivinha de qual time
//	é a rota
//
// and the whole queue was read by gh instead: reported rather than silent, but
// the worse source for a reason nobody asked for. So where the CEO repository
// is, is asked for on its own, and it wins.
//
// Precedence: ceoDir; then CEORoot(kitPath), so a caller that passes only the
// frontier flags keeps the directory it always got; then "", the process's own
// directory, which DirRunner reads as "here" and which is the right answer for
// a board launched from inside the CEO checkout. Nothing is invented.
//
// The problem costs no subprocess because it is a property of the path:
// usina resolves WHICH team's route it is answering from a ceo-<name> segment
// of the working directory, or from USINA_CONFIG. It is returned rather than
// raised because a queue board is more than usina — the gh fallback still
// answers, and a --repo board next to it never needed usina at all — so
// refusing to open would kill a board that works in part.
func WorkDir(ceoDir, kitPath string) (string, error) {
	dir := strings.TrimSpace(ceoDir)
	if dir == "" {
		dir = CEORoot(kitPath)
	}
	return dir, instanceProblem(dir)
}

// ceoSegmentPrefix is how usina names an instance: a path segment `ceo-<name>`
// (ceo-bora, ceo-pp), which a worktree of that checkout keeps as a prefix.
const ceoSegmentPrefix = "ceo-"

// instanceProblem is usina's refusal, predicted from the path alone.
//
// An empty dir is the process's own directory, because that is what DirRunner
// runs the children in, and it is the directory usina would read. A Getwd that
// fails names nothing, and a sentence that names no directory is noise, so it
// says nothing at all.
func instanceProblem(dir string) error {
	// USINA_CONFIG points at the instance yml outright, so the path stops
	// being evidence: usina answers from a directory with no ceo- segment
	// whenever it is set, and a board that complained anyway would be
	// teaching the owner to ignore the problem line.
	if strings.TrimSpace(os.Getenv("USINA_CONFIG")) != "" {
		return nil
	}
	named := dir
	if named == "" {
		here, err := os.Getwd()
		if err != nil {
			return nil
		}
		named = here
	}
	if hasCEOSegment(named) {
		return nil
	}
	return fmt.Errorf("the queue's children run in %s, which has no ceo-<name> path segment: "+
		"usina refuses to route from there (\"instância da usina indeterminada: nenhum segmento "+
		"'ceo-<nome>' no cwd\"), so the issues are read by gh and left unranked — say where the "+
		"CEO repository is, or export USINA_CONFIG with the instance yml", named)
}

// hasCEOSegment answers whether any segment of dir names a CEO instance.
//
// Segments are compared, never the whole string: a path that merely CONTAINS
// "ceo-" (a file named report-ceo-notes.md, a directory called traceo-tmp)
// says nothing about which instance usina would resolve, and a board that
// stayed quiet for one of those would be quiet for the wrong reason.
func hasCEOSegment(dir string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(dir), "/") {
		if name, found := strings.CutPrefix(segment, ceoSegmentPrefix); found && name != "" {
			return true
		}
	}
	return false
}

// charterPath is the project charter kit.py resolves the ranked repository
// from: <CEORoot>/projetos/<slug>/charter.md, measured 2026-09-17 in kit.py's
// own `fronteira` verb.
func charterPath(kitPath, slug string) string {
	root := CEORoot(kitPath)
	if root == "" || strings.TrimSpace(slug) == "" {
		return ""
	}
	return filepath.Join(root, "projetos", strings.TrimSpace(slug), "charter.md")
}

// charterRepo reads the `repo:` front-matter field of a project charter.
//
// The front matter is parsed, never pattern-matched: `repo:` also appears in
// charter prose, and a regexp over the whole file would take dictation from
// whichever line came first.
func charterRepo(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("no charter path to read")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", fmt.Errorf("no front matter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", fmt.Errorf("unterminated front matter")
	}
	var header struct {
		Repo string `yaml:"repo"`
	}
	if err := yaml.Unmarshal([]byte(rest[:end+1]), &header); err != nil {
		return "", err
	}
	repo := strings.TrimSpace(header.Repo)
	if repo == "" {
		return "", fmt.Errorf("no repo: field in its front matter")
	}
	return repo, nil
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

	labels := make([]string, 0, len(card.Labels)+5)
	labels = append(labels, LabelSourceIssue)
	if origin := originLabel(queue.Origin.Source); origin != "" {
		labels = append(labels, origin)
	}
	labels = append(labels, LabelIssuePrefix+strconv.Itoa(card.Number))
	// One of the two, chosen by the one field GitHub answered with, so a
	// card can never wear both.
	if card.Closed {
		labels = append(labels, LabelStateClosed)
	} else {
		labels = append(labels, LabelStateOpen)
	}
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
			// Empty on the usina path: it does not send updatedAt.
			Updated: card.UpdatedAt,
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
// An empty dir is the current directory, so this is always safe to wrap. Which
// dir to wrap is WorkDir's answer, not this function's: a caller that derived
// it from kit.py's path alone was letting the frontier flags decide whether
// usina answered at all.
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
