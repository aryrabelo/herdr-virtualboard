// Package fios reads the two task surfaces a vault owner actually keeps by
// hand — `FIOS.md` (loose threads, one line each) and `gates/*.md` (ledgers of
// verification boxes) — and presents them as feature specs the board can
// render without knowing where they came from.
//
// The surfaces are not ours. Their grammar is defined by `gates/CONVENCAO.md`
// in the vault, and the reference implementation is `bin/fios-check` (python3,
// same vault), which this package deliberately mirrors so the board and the
// session-open scan never disagree about what is open. Divergences from that
// reference are listed on Load.
//
// This package is read-only by construction: it calls os.ReadFile and
// os.ReadDir and nothing else. Writing a card back would mean rewriting
// someone's ledger, which is the one thing the convention forbids
// (CONVENCAO.md:54-57 — turning a box is a human/agent act with measured
// evidence pasted in, never a side effect of a board move).
package fios

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// StaleDays is the age at which an open thread counts as forgotten. Same
// constant as `bin/fios-check:38` (VENCIDO_DIAS); FIOS.md:5 states it in prose.
const StaleDays = 14

// LabelPrefix namespaces every label this source MINTS. A card's labels mix
// two authorities — what hvb concluded and what a file or a repository says —
// and only the prefix keeps them apart: an unprefixed `state:canceled` is an
// opinion anyone can type, so a consumer deriving a column from it would be
// taking dictation from content instead of from the harness.
const LabelPrefix = "hvb:"

// Labels every card from this source can carry. The board treats them as
// opaque strings; the composing backend uses them to decide which cards are
// read-only and which column a terminal card belongs to. These constants are
// the only spelling of these strings — a literal typed a second time is how
// two sources drift apart in silence.
const (
	LabelSourceFios  = LabelPrefix + "source:fios"
	LabelSourceGates = LabelPrefix + "source:gates"
	// LabelInbox marks a line dumped in `## Inbox`: an idea not yet triaged
	// into the DEVE/DESDE/ONDE form (FIOS.md:24-28).
	LabelInbox = LabelPrefix + "inbox"
	// LabelStale marks an open thread parked for StaleDays or more.
	LabelStale = LabelPrefix + "stale"
	// LabelClosed marks a thread already moved to `## Fechados`.
	LabelClosed = LabelPrefix + "closed"
	// LabelResolvedOther marks a `- [-]` box: closed without delivery, which
	// the convention counts as RESOLVED (CONVENCAO.md:15) — the round stops
	// showing up in the open scan. It deliberately does not reuse
	// `hvb:state:canceled`: that label means a pull request closed without
	// merge, and the terminal column derived from it exists to separate
	// "died" from "solved another way". A box superseded by a better design
	// is the second thing, so it lands in Done with its own label.
	LabelResolvedOther = LabelPrefix + "resolved:other"

	// The three prefixes below carry a value read from the owner's files. The
	// KEY is minted and the value is only appended to it, so no file content
	// can impersonate another label: `hvb:ctx:` + anything is still an
	// `hvb:ctx:`. This is why this source needs no discard rule — see Load.
	LabelContextPrefix = LabelPrefix + "ctx:"
	LabelGatePrefix    = LabelPrefix + "gate:"
	LabelGateIDPrefix  = LabelPrefix + "gate-id:"
)

var (
	// Every pattern that counts a box anchors at `^`. The trap is real and
	// recorded in the vault's GATES.md (gate I2): an unanchored CHECK
	// contained the very string it grepped for, counted itself, and inflated
	// 2 into 6. See CONVENCAO.md:22-28.
	reBox = regexp.MustCompile(`^- \[(.)\][ \t]*(.*)$`)
	// Box id at the head of the title: G7, I12, P1, L4a…
	reGateID    = regexp.MustCompile(`^([A-Za-z]{0,3}\d+[a-z]?)[ \t]*[:\-–—][ \t]*(.*)$`)
	reGateField = regexp.MustCompile(`^[ \t]*(CHECK|EXPECT|EVIDENCE|BLOQUEIO|IMPOSS[IÍ]VEL|SUPERADO):[ \t]*(.*)$`)
	reHeading   = regexp.MustCompile(`^#`)

	// A thread line requires DEVE: on it, and the bracketed context is 2+
	// chars — so `- [x] …` (a one-char box marker) never enters as a thread.
	reFio      = regexp.MustCompile(`^-[ \t]+(?:\[([^\]]{2,})\][ \t]*)?(.*DEVE:.*)$`)
	reDeve     = regexp.MustCompile(`DEVE:[ \t]*([^·\n]+)`)
	reOnde     = regexp.MustCompile(`ONDE:[ \t]*([^·\n]+)`)
	reDesde    = regexp.MustCompile(`DESDE:[ \t]*(\S+)`)
	reTitleCut = regexp.MustCompile(`[ \t]—[ \t]|[ \t]DEVE:`)
	reDate     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	// Any top-level thread bullet, used for the closed section where the line
	// carries the outcome instead of a DEVE:.
	reBullet = regexp.MustCompile(`^-[ \t]+(?:\[([^\]]{2,})\][ \t]*)?(.*)$`)

	reInboxHeading  = regexp.MustCompile(`^##[ \t]*Inbox`)
	reClosedHeading = regexp.MustCompile(`^#+[ \t]*Fechados`)

	// Owner lists: "Ary e Marina", "Karen ou Ary", "eu". Same splitter as
	// `bin/fios-check:79`.
	reOwnerSplit = regexp.MustCompile(`\s+e\s+|\s+ou\s+|[/,&+]`)

	limpaReplacer = strings.NewReplacer("**", "", "~~", "", "`", "")
)

// Source is a vault root read as a board.
type Source struct {
	root string
	// now is injected so the stale threshold is testable; production always
	// uses the wall clock.
	now func() time.Time
}

// New returns a Source reading the vault rooted at root. root is the directory
// holding FIOS.md, with the ledgers in `gates/`.
func New(root string) *Source {
	return &Source{root: root, now: time.Now}
}

// Load reads FIOS.md and gates/*.md and returns one spec per thread, per inbox
// line, and per ledger box, plus every problem found on the way.
//
// A broken surface degrades to zero cards from that surface with the reason in
// the error slice; it never aborts the batch, because the board showing eleven
// of twelve ledgers with a visible complaint beats the board showing nothing.
// Errors carry file:line and the fix, not just "malformed".
//
// Known, deliberate divergences from `bin/fios-check`:
//
//   - `GATES.md` and `PLAN.md` are not read. The reference reads both (its
//     `gates()` prepends GATES.md, its `rodadas()` reads PLAN.md). Measured
//     contribution of GATES.md to open work in the vault of 2026-09-17: zero
//     (`grep -c '^- \[ \]' GATES.md` = 0, `'^- \[~\]'` = 0, 7 closed boxes);
//     PLAN.md is an index of rounds, not of work items, so its 3 open rounds
//     have no card shape.
//   - `gates/CONVENCAO.md` is skipped explicitly. The reference lists every
//     `gates/*.md` and gets away with it only because the convention states
//     its box markers inside a markdown table, where the `^- \[` anchor does
//     not reach. Skipping it by name does not depend on that accident.
//   - A thread's status comes from its next-step owner, not from a box: FIOS
//     lines have no markers. `DEVE: eu` is the agent's own debt and therefore
//     actionable (Backlog); any other owner means the thread is parked on a
//     person (Blocked) — which is the whole point of the file ("o primitivo é
//     o DONO DO PROXIMO PASSO", FIOS.md:16-17). The reference splits the same
//     population into `meus` and `travado_em_voce`.
func (s *Source) Load(ctx context.Context) ([]*feature.Spec, []error) {
	var (
		specs []*feature.Spec
		errs  []error
	)

	if err := ctx.Err(); err != nil {
		return nil, []error{err}
	}

	fioSpecs, fioErrs := s.loadFios()
	specs = append(specs, fioSpecs...)
	errs = append(errs, fioErrs...)

	if err := ctx.Err(); err != nil {
		return specs, append(errs, err)
	}

	gateSpecs, gateErrs := s.loadGates(ctx)
	specs = append(specs, gateSpecs...)
	errs = append(errs, gateErrs...)

	errs = append(errs, dedupe(specs)...)

	return specs, errs
}

// dedupe disambiguates the rare case of two items whose normalised text is
// byte-identical, which would otherwise hand two cards the same id. The first
// occurrence in document order keeps the bare id; the next ones get a counter.
// Reported, because two identical lines in a surface is a human mistake worth
// seeing.
func dedupe(specs []*feature.Spec) []error {
	var errs []error
	seen := make(map[string]int, len(specs))
	for _, spec := range specs {
		seen[spec.ID]++
		if n := seen[spec.ID]; n > 1 {
			errs = append(errs, fmt.Errorf("%s: dois itens com o mesmo texto normalizado (%q); o segundo virou %s-%d", spec.Path, spec.Title, spec.ID, n))
			spec.ID = fmt.Sprintf("%s-%d", spec.ID, n)
		}
	}
	return errs
}

// section is where in FIOS.md the reader currently stands. The file is an
// index with three regions and the region decides the meaning of a line.
type section int

const (
	sectionPreamble section = iota
	sectionInbox
	sectionOpen
	sectionClosed
)

func (s *Source) loadFios() ([]*feature.Spec, []error) {
	path := filepath.Join(s.root, "FIOS.md")
	lines, err := readLines(path)
	if err != nil {
		return nil, []error{err}
	}

	var (
		specs []*feature.Spec
		errs  []error
		where = sectionPreamble
		today = s.now()
		abs   = absolute(path)
	)

	for i, line := range lines {
		lineNo := i + 1

		switch {
		case reClosedHeading.MatchString(line):
			where = sectionClosed
			continue
		case where == sectionClosed:
			// Nothing reopens after `## Fechados`; the reference stops
			// reading the file entirely at that heading.
		case reInboxHeading.MatchString(line):
			where = sectionInbox
			continue
		case reHeading.MatchString(line):
			where = sectionOpen
			continue
		case where == sectionInbox && strings.TrimRight(line, " \t") == "---":
			// The inbox ends at the rule before `## Abertos`; without this
			// the rule itself would be read as an untriaged idea.
			where = sectionPreamble
			continue
		}

		if where == sectionClosed {
			// Closed threads have no DEVE:; they carry the outcome and the
			// date instead. Indented lines are annotations of the thread
			// above (a SUPERADO note), not threads.
			if !strings.HasPrefix(line, "- ") {
				continue
			}
			specs = append(specs, closedFio(line, abs))
			continue
		}

		if found := reFio.FindStringSubmatch(line); found != nil {
			spec, fioErrs := s.fioSpec(found[1], found[2], where == sectionInbox, abs, lineNo, today)
			specs = append(specs, spec)
			errs = append(errs, fioErrs...)
			continue
		}

		if where == sectionInbox {
			text := strings.TrimSpace(line)
			if text == "" || strings.HasPrefix(text, "<!--") {
				continue
			}
			specs = append(specs, inboxSpec(text, abs))
		}
	}

	return specs, errs
}

// fioSpec builds the card for one `- [contexto] titulo — DEVE: quem · DESDE:
// AAAA-MM-DD · ONDE: caminho` line (FIOS.md:19, CONVENCAO.md:52).
func (s *Source) fioSpec(ctx, rest string, inbox bool, abs string, lineNo int, today time.Time) (*feature.Spec, []error) {
	var errs []error

	title := rest
	if cut := reTitleCut.FindStringIndex(rest); cut != nil {
		title = rest[:cut[0]]
	}

	owner := field(reDeve, rest)
	if owner == "" {
		// Unreachable through reFio, which requires DEVE: on the line, but a
		// `DEVE:` with nothing after it would land here.
		errs = append(errs, fmt.Errorf("%s:%d: fio sem dono; a linha precisa de `DEVE: <quem tem o proximo passo>` (FIOS.md:19)", abs, lineNo))
	}

	status := feature.Blocked
	if isMine(owner) {
		// `DEVE: eu` is the agent's own debt: nobody else has to move first.
		status = feature.Backlog
	}

	labels := []string{LabelSourceFios}
	if ctx != "" {
		// The bracketed context is the owner's own grouping key ([cnb],
		// [jarvis], [claudinha]); it is the only cross-cutting axis the file
		// has, so it survives as a label rather than being dropped.
		labels = append(labels, LabelContextPrefix+limpa(ctx))
	}
	if inbox {
		labels = append(labels, LabelInbox)
	}

	desde := field(reDesde, rest)
	var since time.Time
	if desde != "" {
		parsed, err := time.Parse("2006-01-02", desde)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s:%d: DESDE invalido %q; use `DESDE: AAAA-MM-DD` (FIOS.md:19)", abs, lineNo, desde))
			desde = ""
		} else {
			since = parsed
		}
	}
	if !since.IsZero() && int(today.Sub(since).Hours()/24) >= StaleDays {
		labels = append(labels, LabelStale)
	}

	onde := field(reOnde, rest)
	body := body(
		pair("DESDE", desde),
		pair("ONDE", onde),
		pair("NOTA", note(rest)),
	)

	return &feature.Spec{
		Frontmatter: feature.Frontmatter{
			ID:      threadID(ctx, title),
			Title:   limpa(title),
			Status:  status,
			Owner:   limpa(owner),
			Created: desde,
			Labels:  labels,
		},
		Path: abs,
		Body: body,
	}, errs
}

// inboxSpec builds the card for an untriaged inbox line. It has no format on
// purpose: demanding DEVE/DESDE/ONDE at dump time is what stops the idea from
// being written down at all (FIOS.md:24-28). Triaging it is actionable work
// for the agent, so it lands in Backlog with nobody parked on it.
func inboxSpec(text, abs string) *feature.Spec {
	title := limpa(strings.TrimLeft(text, "-* \t"))
	return &feature.Spec{
		Frontmatter: feature.Frontmatter{
			ID:     threadID("", title),
			Title:  title,
			Status: feature.Backlog,
			Labels: []string{LabelSourceFios, LabelInbox},
		},
		Path: abs,
		Body: "NOTA: ideia crua, ainda nao triada para o formato DEVE/DESDE/ONDE",
	}
}

// closedFio builds the card for a `## Fechados` line: `- [ctx] ~~titulo~~ —
// AAAA-MM-DD · desfecho`. Closing is explicit in this file precisely so a
// closed thread stays distinguishable from a forgotten one (FIOS.md:21-22),
// which is why these are cards and not silence.
//
// The id is derived from the title, and limpa strips the `~~` the owner wraps
// a closed thread in — so a thread keeps its id when it moves from `##
// Abertos` to `## Fechados`, and a dispatched run still points at it.
func closedFio(line, abs string) *feature.Spec {
	rest, ctx := line, ""
	if found := reBullet.FindStringSubmatch(line); found != nil {
		ctx, rest = found[1], found[2]
	}

	title, outcome := rest, ""
	if cut := reTitleCut.FindStringIndex(rest); cut != nil {
		title, outcome = rest[:cut[0]], strings.TrimSpace(rest[cut[1]:])
	}

	closed := reDate.FindString(outcome)

	return &feature.Spec{
		Frontmatter: feature.Frontmatter{
			ID:      threadID(ctx, limpa(title)),
			Title:   limpa(title),
			Status:  feature.Done,
			Created: closed,
			Updated: closed,
			Labels:  []string{LabelSourceFios, LabelClosed},
		},
		Path: abs,
		Body: body(pair("DESFECHO", outcome)),
	}
}

func (s *Source) loadGates(ctx context.Context) ([]*feature.Spec, []error) {
	dir := filepath.Join(s.root, "gates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("pasta de ledgers ilegivel: %s: %w", dir, err)}
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		// The convention is not a ledger: it states the markers it defines,
		// and counting them would invent open work out of documentation.
		if name == "CONVENCAO.md" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var (
		specs []*feature.Spec
		errs  []error
	)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return specs, append(errs, err)
		}
		fileSpecs, fileErrs := gateFile(filepath.Join(dir, name), name)
		specs = append(specs, fileSpecs...)
		errs = append(errs, fileErrs...)
	}
	return specs, errs
}

// gateBox is one box plus the convention lines that belong to it. A line
// belongs to the box above it until the next box or the next heading — the
// same ownership rule `bin/fios-check:185-194` uses.
type gateBox struct {
	line   int
	marker string
	id     string
	title  string
	fields []string
	seen   map[string]string
}

func gateFile(path, name string) ([]*feature.Spec, []error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, []error{err}
	}

	abs := absolute(path)
	slug := strings.TrimSuffix(name, ".md")

	var (
		boxes   []*gateBox
		current *gateBox
	)
	for i, line := range lines {
		if found := reBox.FindStringSubmatch(line); found != nil {
			box := &gateBox{line: i + 1, marker: found[1], title: found[2], seen: map[string]string{}}
			if id := reGateID.FindStringSubmatch(box.title); id != nil {
				box.id, box.title = id[1], id[2]
			}
			current = box
			boxes = append(boxes, box)
			continue
		}
		if current == nil {
			continue
		}
		if reHeading.MatchString(line) {
			current = nil
			continue
		}
		if found := reGateField.FindStringSubmatch(line); found != nil {
			key := strings.ReplaceAll(found[1], "Í", "I")
			current.seen[key] = found[2]
			current.fields = append(current.fields, strings.TrimSpace(line))
		}
	}

	var (
		specs []*feature.Spec
		errs  []error
	)
	for _, box := range boxes {
		spec, boxErrs := box.spec(slug, name, abs)
		if spec == nil {
			errs = append(errs, boxErrs...)
			continue
		}
		specs = append(specs, spec)
		errs = append(errs, boxErrs...)
	}
	return specs, errs
}

// spec maps one box to a card. The state is read from the marker character
// alone (CONVENCAO.md:8-15), so accented spelling on the text lines
// (IMPOSSIVEL/IMPOSSÍVEL) changes no classification.
func (b *gateBox) spec(slug, name, abs string) (*feature.Spec, []error) {
	var errs []error

	status, extra, ok := boxStatus(b.marker)
	if !ok {
		return nil, []error{fmt.Errorf("%s:%d: marcador de caixa %q desconhecido; a convencao tem exatamente `- [x]`, `- [ ]`, `- [~]` e `- [-]` (CONVENCAO.md:8-15)", abs, b.line, b.marker)}
	}

	labels := []string{LabelSourceGates, LabelGatePrefix + name}
	if b.id != "" {
		labels = append(labels, LabelGateIDPrefix+b.id)
	}
	if extra != "" {
		labels = append(labels, extra)
	}

	owner := ""
	switch b.marker {
	case " ":
		if _, has := b.seen["CHECK"]; !has {
			errs = append(errs, fmt.Errorf("%s:%d: caixa `- [ ]` sem CHECK:; caixa acionavel precisa de `CHECK:` + `EXPECT:`, ou vira `- [~]` com dono (CONVENCAO.md:11)", abs, b.line))
		}
		if _, has := b.seen["EXPECT"]; !has {
			errs = append(errs, fmt.Errorf("%s:%d: caixa `- [ ]` sem EXPECT:; sem o esperado ninguem pode virar a caixa (CONVENCAO.md:11)", abs, b.line))
		}
	case "~":
		bloqueio, has := b.seen["BLOQUEIO"]
		if !has {
			errs = append(errs, fmt.Errorf("%s:%d: caixa `- [~]` sem BLOQUEIO:; escreva `BLOQUEIO: <dono> · <o que exatamente destrava>` (CONVENCAO.md:12)", abs, b.line))
			break
		}
		who, _, found := strings.Cut(bloqueio, "·")
		owner = limpa(who)
		if !found || owner == "" {
			errs = append(errs, fmt.Errorf("%s:%d: BLOQUEIO sem `<dono> · <o que destrava>`; tem %q (CONVENCAO.md:12)", abs, b.line, bloqueio))
		}
		if _, has := b.seen["EVIDENCE"]; has {
			errs = append(errs, fmt.Errorf("%s:%d: caixa `- [~]` com EVIDENCE:; caixa bloqueada nao tem evidencia — se a evidencia existe a caixa e `- [x]` (CONVENCAO.md:12)", abs, b.line))
		}
	case "x", "X":
		if _, has := b.seen["EVIDENCE"]; !has {
			errs = append(errs, fmt.Errorf("%s:%d: caixa `- [x]` sem EVIDENCE:; feche com a saida medida colada, ou volte para `- [~]` com dono (CONVENCAO.md:10,17-20)", abs, b.line))
		}
	case "-":
		_, impossivel := b.seen["IMPOSSIVEL"]
		_, superado := b.seen["SUPERADO"]
		if !impossivel && !superado {
			errs = append(errs, fmt.Errorf("%s:%d: caixa `- [-]` sem motivo; escreva `IMPOSSIVEL: <motivo + fonte>` ou `SUPERADO: <motivo + fonte>` (CONVENCAO.md:13)", abs, b.line))
		}
	}

	return &feature.Spec{
		Frontmatter: feature.Frontmatter{
			ID:     boxID(slug, b.id, limpa(b.title)),
			Title:  limpa(b.title),
			Status: status,
			Owner:  owner,
			Labels: labels,
		},
		Path: abs,
		Body: strings.Join(b.fields, "\n"),
	}, errs
}

// boxStatus maps a marker character to the board status, plus the extra label
// the status alone cannot carry. `- [-]` is terminal and, per CONVENCAO.md:15,
// RESOLVED — so it is plain Done, marked LabelResolvedOther for the nuance and
// never `state:canceled`, which belongs to a pull request that died.
func boxStatus(marker string) (feature.Status, string, bool) {
	switch marker {
	case "x", "X":
		return feature.Done, "", true
	case " ":
		return feature.Backlog, "", true
	case "~":
		return feature.Blocked, "", true
	case "-":
		return feature.Done, LabelResolvedOther, true
	default:
		return "", "", false
	}
}

// isMine reports whether the next step is the agent's own. Same rule as
// `bin/fios-check:82-85`: without it a thread the agent just created only
// surfaces when it goes stale, which is 14 days too late.
func isMine(owner string) bool {
	for _, token := range reOwnerSplit.Split(owner, -1) {
		if strings.EqualFold(strings.TrimSpace(token), "eu") {
			return true
		}
	}
	return false
}

// note is the measured detail the owner appends after the named fields: the
// `·` separated chunks that are not DEVE/DESDE/ONDE. The title is excluded by
// starting at the first field delimiter rather than by filtering, because a
// title may itself contain a `·`.
func note(rest string) string {
	region := rest
	if cut := reTitleCut.FindStringIndex(rest); cut != nil {
		region = rest[cut[0]:]
	}
	var out []string
	for _, chunk := range strings.Split(region, "·") {
		chunk = strings.Trim(strings.TrimSpace(chunk), "—")
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if reDeve.MatchString(chunk) || reOnde.MatchString(chunk) || reDesde.MatchString(chunk) {
			continue
		}
		out = append(out, chunk)
	}
	return strings.Join(out, " · ")
}

// threadID and boxID derive a card id from the item's own text instead of its
// position in the file.
//
// Position cannot be the identity here: inserting one thread at the top of
// FIOS.md would renumber every thread below it, and `runs.Run.FeatureID`
// (internal/runs/store.go:57) binds a dispatched run to that id — so a live
// run would silently start pointing at a different thread, with the board
// showing the work on the wrong card. Nothing may be written back to the
// owner's file to fix that (this source is read-only), so the text itself is
// the only stable key available.
//
// The trade the hash makes: rewording an item is a new id, and a run bound to
// the old wording orphans visibly instead of being silently reassigned. That
// is the failure everyone wants — loud and wrong-looking, not quiet and wrong.
//
// Uniqueness is by construction, never by luck. Every discriminant that
// separates two same-titled items goes into the hash material: the bracketed
// context for a thread, the ledger slug for a box. Whatever still ties is
// broken by dedupe, in document order.
func threadID(ctx, text string) string {
	// The context bracket ([cnb], [jarvis]) is the owner's own partition of
	// FIOS.md, so two threads titled the same in different contexts are
	// different cards. It is part of the material rather than of the visible
	// id because the id is already long enough to read on a card.
	return "FIO-" + shortHash(normalizeID(ctx), normalizeID(text))
}

// boxID keys a gate box on its ledger plus, when the box declares one, the id
// the owner wrote (G7, I12, P1). That id is the owner's own stable handle, so
// rewording the box title keeps the card — and with it any run bound to it.
// The ledger slug is in the material as well as in the prefix, so "rodar o
// gate" in two ledgers cannot alias.
func boxID(slug, declared, title string) string {
	key := normalizeID(title)
	if declared != "" {
		key = strings.ToLower(declared)
	}
	return "GATE-" + slug + "-" + shortHash(slug, key)
}

// shortHash is 4 bytes of SHA-256 over the parts, NUL-joined so no part can
// bleed into the next. 8 hex characters keep the card id readable on a narrow
// column; the residual tie is handled by dedupe rather than by a longer id.
func shortHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:4])
}

// normalizeID strips the markdown emphasis, the case, and the run-length of
// whitespace, so reflowing a line or bolding a word does not mint a new card.
func normalizeID(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(limpa(text)), " "))
}

type keyed struct {
	key   string
	value string
}

func pair(key, value string) keyed { return keyed{key: key, value: value} }

// body renders the present fields only. A missing field is an absent line, not
// an empty one, so nothing downstream has to distinguish "" from unset.
func body(fields ...keyed) string {
	var out []string
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			continue
		}
		out = append(out, field.key+": "+limpa(field.value))
	}
	return strings.Join(out, "\n")
}

func field(re *regexp.Regexp, line string) string {
	found := re.FindStringSubmatch(line)
	if found == nil {
		return ""
	}
	return strings.TrimSpace(found[1])
}

// limpa strips the markdown emphasis the owner writes inline and the
// punctuation left over at a cut. Same normalisation as `bin/fios-check:67-68`.
func limpa(text string) string {
	return strings.Trim(limpaReplacer.Replace(text), " .;:—-")
}

// readLines reads a surface. Read-only: os.ReadFile is the only file access in
// this package besides os.ReadDir.
func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("superficie ilegivel: %s: %w", path, err)
	}
	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), nil
}

// absolute resolves a surface path for display. A relative root is a caller
// mistake, not a reason to drop the card, so the joined path is the fallback.
func absolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
