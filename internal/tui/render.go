package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// Render produces the whole frame: exactly height lines, each at most width
// display columns. It is pure, so the golden tests drive it directly.
func (m *Model) Render() []string {
	if m.width < minWidth || m.height < minHeight {
		return []string{truncate("Terminal too small for the board.", m.width)}
	}
	switch m.view {
	case ViewHelp:
		return m.frame(m.renderHelp())
	case ViewDetail:
		return m.frame(m.renderDetail())
	case ViewNewFeature, ViewMovePicker, ViewDispatchPicker, ViewConfirm:
		// Overlays draw on top of the board so the user keeps their place.
		return m.frame(m.overlay(m.renderBoard()))
	default:
		return m.frame(m.renderBoard())
	}
}

// frame wraps a body in the header and footer and pads to the full height.
func (m *Model) frame(body []string) []string {
	out := make([]string, 0, m.height)
	out = append(out, m.renderHeader())
	bodyHeight := m.height - 2
	for index := 0; index < bodyHeight; index++ {
		if index < len(body) {
			out = append(out, body[index])
			continue
		}
		out = append(out, "")
	}
	out = append(out, m.renderFooter())
	return out
}

func (m *Model) renderHeader() string {
	counts := m.Counts()
	total := 0
	for _, count := range counts {
		total += count
	}
	left := paint(m.palette.Bold, " VirtualBoard ") + paint(m.palette.Accent, m.backend.ProjectName())
	if m.Reloading() {
		// A backend read can take a minute; without this the board looks
		// frozen, not busy. Text, so it stays legible on an empty board and
		// under NO_COLOR; Running is the same blue the board already uses
		// for work in flight.
		left += paint(m.palette.Running, " reading…")
	}

	// Two cells are spoken for whatever the summary says: the gap that
	// separates the two halves, and the trailing space the header has
	// always ended with.
	right := m.headerSummary(counts, total, m.width-displayWidth(left)-2)
	gap := m.width - displayWidth(left) - displayWidth(right) - 1
	if gap < 1 {
		return fit(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

// headerSummary is the right-hand half of the header — the live-run marker, a
// count per drawn column, and the board total — built to fit budget cells.
//
// It has to fit rather than overflow, because a declared line can be twelve
// columns long: at 80 columns the full trail is wider than the terminal, and
// the header used to answer that by dropping the whole right-hand side, taking
// with it the one number a reader cannot recover by looking at the board — the
// total. So the counts are what gives way, in order: the empty columns first,
// then whole columns from the left with `…` marking the cut. The run marker
// and the total always survive.
func (m *Model) headerSummary(counts map[feature.Status]int, total, budget int) string {
	prefix := ""
	if active := m.ActiveRunCount(); active > 0 {
		prefix = paint(m.palette.Running, fmt.Sprintf("▶%d ", active))
	}
	suffix := fmt.Sprintf("  %d", total)
	room := budget - displayWidth(prefix) - displayWidth(suffix)

	separator := paint(m.palette.Border, " · ")
	needs := m.needsAnswerCounts()
	segments := m.headerCounts(counts, needs, m.compact())
	joined := strings.Join(segments, separator)
	if displayWidth(joined) > room && !m.compact() {
		// A zero is the least a reader loses: the column is on screen
		// and visibly empty.
		segments = m.headerCounts(counts, needs, true)
		joined = strings.Join(segments, separator)
	}
	elision := paint(m.palette.Border, "… ")
	for displayWidth(joined) > room && len(segments) > 1 {
		// Whole segments, never a cut through one: truncate() stops
		// mid-string and would leave a colour open, bleeding it across
		// the rest of the header.
		segments = segments[1:]
		joined = elision + strings.Join(segments, separator)
	}
	if displayWidth(joined) > room {
		joined = ""
	}
	return prefix + joined + suffix
}

// headerCounts is one painted `SHORT n` per drawn column, optionally without
// the empty ones.
func (m *Model) headerCounts(counts, needs map[feature.Status]int, skipEmpty bool) []string {
	order := m.columnOrder()
	segments := make([]string, 0, len(order))
	for _, status := range order {
		if skipEmpty && counts[status] == 0 {
			continue
		}
		segment := paint(m.palette.Status(status, m.workflow.Index(status)),
			fmt.Sprintf("%s %d", m.workflow.Short(status), counts[status]))
		segment += m.needsAnswerShare(needs[status], counts[status])
		segments = append(segments, segment)
	}
	return segments
}

// needsAnswerCounts is how many cards in each column are waiting on the owner
// himself. Per column and not one board-wide number, because the board has to
// say WHERE they are: fila.ColumnFor sends every open `hitl` issue to Blocked,
// but a hand-written VirtualBoard spec can carry the label in any status, and a
// single total would put those cards nowhere.
func (m *Model) needsAnswerCounts() map[feature.Status]int {
	out := map[feature.Status]int{}
	for _, status := range m.columnOrder() {
		for _, card := range m.columns[status] {
			if m.cardNeedsAnswer(card.Spec) {
				out[status]++
			}
		}
	}
	return out
}

// needsAnswerShare is what a column's header segment adds when some of its
// cards are waiting on the owner.
//
// It is a share of an existing count, not a counter of its own. Measured on
// the owner's queue (2026-09-18), all 6 cards in BLOCKED are `hitl`, so a
// separate `YOU 6` beside `BLOCK 6` would print the same digit twice and
// teach the eye to skip one of them. So: the glyph alone when the whole
// column is his to answer (`BLOCK 6◆` — the digit is already on screen), and
// the glyph with a number when it is a part of the column (`BLOCK 6 ◆2`),
// which is the shape the header takes the day kit.py blocks a card that is
// not his.
func (m *Model) needsAnswerShare(needs, total int) string {
	switch {
	case needs <= 0:
		return ""
	case needs >= total:
		return paint(m.palette.NeedsAnswer, needsAnswerGlyph)
	default:
		return paint(m.palette.NeedsAnswer, " "+needsAnswerGlyph+strconv.Itoa(needs))
	}
}

func (m *Model) renderFooter() string {
	if message, isError, ok := m.footerMessage(); ok {
		colour := m.palette.Dim
		if isError {
			colour = m.palette.Warn
		}
		return paint(colour, fit(" "+message, m.width))
	}
	return paint(m.palette.Dim, fit(" "+m.keyHint(), m.width))
}

func (m *Model) footerMessage() (string, bool, bool) {
	if message, isError := m.statusMessage(); message != "" {
		return message, isError, true
	}
	// An empty board is the one state a user cannot read off the columns: a
	// clean queue and a source that returned nothing draw the same dashes.
	// Measured: pointing the board at an unreachable repository dropped 39
	// cards and said "1 spec(s) could not be read", which reads like one bad
	// file rather than a whole source that never answered.
	if m.Empty() {
		if len(m.problems) > 0 {
			return fmt.Sprintf("no cards, and %d problem(s) — a source failed rather than came back empty; press ? for detail", len(m.problems)), true, true
		}
		// The hint gets the whole line, not a preamble: the footer is
		// truncated at the terminal width, and a preamble spends that
		// budget on prose while cutting off the command the user needs
		// (measured at 120 columns: "`hvb queue --va…").
		if m.emptyHint != "" {
			return m.emptyHint, false, true
		}
		return "no cards, and every source answered: nothing is open", false, true
	}
	if len(m.problems) > 0 {
		// Not "spec(s)": a problem is an unreadable spec, a failed
		// reconcile, a dead run store, or an entire source, and naming
		// them all specs misstates what went wrong and how much.
		return fmt.Sprintf("%d problem(s) — press ? for detail", len(m.problems)), true, true
	}
	return "", false, false
}

func (m *Model) keyHint() string {
	switch m.view {
	case ViewDetail:
		return "esc back · e edit · d dispatch · m move · o focus pane · x cancel run · ? help"
	case ViewHelp:
		return "esc back"
	default:
		if m.compact() {
			return "↑↓ card · ←→ column · ⏎ detail · d dispatch · ? help · q quit"
		}
		return "←→/hl column · ↑↓/jk card · ⏎ detail · n new · m move · H/L shift · d dispatch · r refresh · ? help · q quit"
	}
}

// compact reports whether the board is on a narrow terminal — a phone, a split
// pane — where only one column fits, and should show that one instead of a row
// of unreadable slivers.
func (m *Model) compact() bool { return m.visibleColumns() < 2 }

// minColumnWidth is how narrow a column can be and still read: a card needs
// room for an id, an owner and an age on one row. It is what decides how many
// of a twelve-column line fit on screen at once.
const minColumnWidth = 22

// visibleColumns is how many columns the terminal has room for, whatever the
// line declares. A declared line can be twelve columns long; a terminal cannot.
func (m *Model) visibleColumns() int {
	fit := m.width / minColumnWidth
	if fit < 1 {
		return 1
	}
	if order := len(m.columnOrder()); fit > order {
		return order
	}
	return fit
}

func (m *Model) renderBoard() []string {
	if m.compact() {
		return m.renderSingleColumn()
	}
	return m.renderColumns()
}

// renderColumns draws a window of columns side by side, centred on the focused
// one.
//
// A window rather than every column, because a twelve-column line does not fit
// on any terminal anyone uses: five columns at 22 each already wants 110, and
// the alternative — dividing the width by twelve — produces nine-column cells
// that can hold neither an id nor a title. The breadcrumb above names every
// declared column, so the window never costs the user their place.
func (m *Model) renderColumns() []string {
	statuses := m.columnOrder()
	height := m.height - 2
	start, end := m.columnWindow()
	visible := statuses[start:end]

	var out []string
	if end-start < len(statuses) {
		out = append(out, m.renderBreadcrumb(statuses, start, end))
		height--
	}
	widths := splitWidth(m.width, len(visible))

	rendered := make([][]string, len(visible))
	for index, status := range visible {
		rendered[index] = m.renderColumn(status, widths[index], height, start+index == m.column)
	}

	for row := 0; row < height; row++ {
		var line strings.Builder
		for index := range visible {
			if row < len(rendered[index]) {
				line.WriteString(rendered[index][row])
			} else {
				line.WriteString(strings.Repeat(" ", widths[index]))
			}
		}
		out = append(out, line.String())
	}
	return out
}

// columnWindow is the half-open range of columns to draw: as many as fit,
// centred on the focused one and clamped to the ends, so walking right scrolls
// the board instead of losing the cursor off the edge.
func (m *Model) columnWindow() (int, int) {
	statuses := m.columnOrder()
	visible := m.visibleColumns()
	start := m.column - visible/2
	if start > len(statuses)-visible {
		start = len(statuses) - visible
	}
	if start < 0 {
		start = 0
	}
	return start, start + visible
}

// renderSingleColumn is the narrow layout: one column, with the line shown as a
// breadcrumb so the user still knows where they are.
func (m *Model) renderSingleColumn() []string {
	status := m.FocusedStatus()
	height := m.height - 2
	body := m.renderColumn(status, m.width, height-1, true)

	out := make([]string, 0, height)
	out = append(out, m.renderBreadcrumb(m.columnOrder(), m.column, m.column+1))
	out = append(out, body...)
	return out
}

// renderBreadcrumb names the columns the board draws, marking the focused one
// and showing `‹`/`›` when the window leaves columns off screen. It is the only
// thing telling a user of a windowed board that the line continues.
//
// A trail wider than the terminal is fitted around the cursor rather than cut
// off at the right. Twelve columns at 43 cells have room for about five
// labels, and truncating the string lost whichever came last — which, walking
// right, is the focused label itself and the `›` that says the line goes on.
// Both markers and the focused label always survive; `…` stands for the labels
// dropped from either end.
func (m *Model) renderBreadcrumb(statuses []feature.Status, start, end int) string {
	if len(statuses) == 0 {
		return fit("", m.width)
	}
	labels := make([]string, len(statuses))
	colours := make([]string, len(statuses))
	for index, candidate := range statuses {
		labels[index] = " " + m.workflow.Short(candidate) + " "
		switch {
		case index == m.column:
			colours[index] = m.palette.Invert
		case index < start || index >= end:
			// Off-screen columns stay in the line but read as absent,
			// so the trail is a map rather than a promise.
			colours[index] = m.palette.Border
		default:
			colours[index] = m.palette.Dim
		}
	}
	head, tail := "", ""
	if start > 0 {
		head = paint(m.palette.Border, "‹")
	}
	if end < len(statuses) {
		tail = paint(m.palette.Border, "›")
	}
	// The markers are reserved first: they are the only thing that says the
	// window is a window, and they cost one cell each.
	trail := m.crumbTrail(labels, colours, m.width-displayWidth(head)-displayWidth(tail))
	return fit(head+trail+tail, m.width)
}

// crumbTrail is as many of the labels as fit in width, centred on the focused
// one and grown outward a label at a time, with `…` where an end was dropped.
//
// The labels arrive unpainted, with their colours beside them, so the width
// arithmetic is over plain text and nothing is ever cut between a colour and
// its reset.
func (m *Model) crumbTrail(labels, colours []string, width int) string {
	focus := m.column
	if focus < 0 {
		focus = 0
	}
	if focus >= len(labels) {
		focus = len(labels) - 1
	}
	widths := make([]int, len(labels))
	for index, label := range labels {
		widths[index] = displayWidth(label)
	}
	// cost is what drawing [first,last] takes, including the elision marks
	// the dropped ends imply.
	cost := func(first, last int) int {
		total := 0
		for index := first; index <= last; index++ {
			total += widths[index]
		}
		if first > 0 {
			total++
		}
		if last < len(labels)-1 {
			total++
		}
		return total
	}
	first, last := focus, focus
	if cost(first, last) > width {
		// Not even the focused label fits whole. Cut, it still says where
		// the cursor is, which an empty trail does not.
		return paint(colours[focus], truncate(labels[focus], width))
	}
	for {
		grew := false
		if first > 0 && cost(first-1, last) <= width {
			first--
			grew = true
		}
		if last < len(labels)-1 && cost(first, last+1) <= width {
			last++
			grew = true
		}
		if !grew {
			break
		}
	}
	var trail strings.Builder
	mark := paint(m.palette.Border, "…")
	if first > 0 {
		trail.WriteString(mark)
	}
	for index := first; index <= last; index++ {
		trail.WriteString(paint(colours[index], labels[index]))
	}
	if last < len(labels)-1 {
		trail.WriteString(mark)
	}
	return trail.String()
}

// focusCaretGlyph marks the focused column — its title bar and, when the
// column is empty, its placeholder. It is U+25B8 BLACK RIGHT-POINTING SMALL
// TRIANGLE, which the owner's terminal font
// (~/Library/Fonts/JetBrainsMonoNerdFontMono-Regular.ttf) maps to glyph 1238.
// Measured by parsing the font's cmap the same way the needsAnswer diamond
// was: the parser reports the control U+25C6 as glyph 1252 (present) and
// U+25D0 as glyph 0 (absent), so it reads per-codepoint coverage, not a whole
// Unicode block. It is text, not an SGR, so column focus stays visible under
// NO_COLOR.
const focusCaretGlyph = "▸"

// renderColumn draws one lifecycle column into a fixed width and height.
//
// The body is built at the inner width and the separator is appended to every
// row at the end, including the blank ones. Appending it only to rows that have
// content leaves the vertical rule stopping below the last card, which reads as
// a broken frame rather than an empty column.
func (m *Model) renderColumn(status feature.Status, width, height int, focused bool) []string {
	cards := m.Cards(status)
	inner := width - 1
	if inner < 8 {
		inner = width
	}

	body := make([]string, 0, height)
	title := fmt.Sprintf("%s %d", strings.ToUpper(m.workflow.Title(status)), len(cards))
	titleColour := m.palette.Status(status, m.workflow.Index(status))
	if focused {
		// The old signal was `+= Bold`, and Bold is an SGR: under NO_COLOR
		// it is the empty string, so a colour-blind terminal got no focus
		// mark at all — and on a zero-card column there is not even a
		// selected card to invert. The fix has to show in both places. The
		// caret is text (it survives NO_COLOR), and Invert turns the whole
		// title into a solid block of the column's own status hue — the
		// "shine" the owner asked for. focusCaretGlyph carries its own
		// font-coverage proof.
		title = focusCaretGlyph + " " + title
		titleColour += m.palette.Invert
	}
	body = append(body, paint(titleColour, fit(title, inner)))
	body = append(body, paint(m.palette.Border, strings.Repeat("─", inner)))

	if column := m.backend.Config().Column(status); column.Auto {
		body = append(body, paint(m.palette.Dim, fit(" auto → "+column.OnSuccess, inner)))
	}

	if visible := height - len(body); visible > 0 {
		selected := m.card[status]
		start := scrollWindow(selected, len(cards), visible/cardHeight)
		for index := start; index < len(cards); index++ {
			block := m.renderCard(cards[index], inner, focused && index == selected)
			if len(body)+len(block) > height {
				break
			}
			body = append(body, block...)
		}
		if len(cards) == 0 && len(body) < height {
			placeholder := paint(m.palette.Dim, fit(" —", inner))
			if focused {
				// The empty column has to declare its focus too: a focused
				// empty column and an unfocused one used to be byte-for-byte
				// identical — the exact column the owner watched stop
				// "brilhando" the day REVIEW hit zero cards. Same caret +
				// Invert as the title, so the whole empty column reads as one
				// highlighted block, with colour and without it.
				placeholder = paint(titleColour, fit(" "+focusCaretGlyph+" —", inner))
			}
			body = append(body, placeholder)
		}
	}

	out := make([]string, height)
	gutter := m.gutter(width, inner)
	for index := 0; index < height; index++ {
		line := strings.Repeat(" ", inner)
		if index < len(body) {
			line = body[index]
		}
		out[index] = line + gutter
	}
	return out
}

// cardHeight is how many rows one card occupies: header, title, chips, spacer.
const cardHeight = 4

func (m *Model) gutter(width, inner int) string {
	if width <= inner {
		return ""
	}
	return paint(m.palette.Border, strings.Repeat("│", 1)) + strings.Repeat(" ", width-inner-1)
}

// renderCard draws one card as four rows, in the shape the owner approved
// from the Factory board (2026-09-17):
//
//	▸ ceo-bora#150 (issue) · @aryrabelo · 1d        ← who and what, dim
//	  Pagar a divida de lint do codigo importado…   ← the title, on its own
//	  rumo:grilling  project:bugtoprompt            ← chips: state the reader acts on
//
// The header names the card (id, kind, owner, age, the issue a PR closes) so
// the reader knows what they are looking at before reading a word of the
// title; the title gets a whole row because a 26-column cell cannot hold an
// id and a sentence side by side; and the chips row carries every label the
// sources did not mint for themselves, since `hvb:*` labels are the board's
// own bookkeeping and mean nothing to the person reading.
//
// A card waiting on the owner's own hands gets two marks of its own, because
// he asked for exactly that — "tem que ser claro que tá precisando de uma
// resposta minha":
//
//	◆ ceo-bora#160 (issue) · @aryrabelo · 3d
//	  Mintar a credencial da factory
//	  ◆ NEEDS YOU  project:bugtoprompt
//
// The badge leads the chip row so a 26-column card truncates the labels
// instead of the badge, and the gutter glyph puts the same mark at the card's
// left edge, where the eye scans a column.
func (m *Model) renderCard(card *Card, width int, selected bool) []string {
	needsAnswer := m.cardNeedsAnswer(card.Spec)
	marker := " "
	markerColour := ""
	switch {
	case card.Active != nil:
		// A live run is fresher news than a standing question, and after
		// the unblocker lands a `hitl` card can have one. The chip row
		// still carries the badge, so nothing is lost.
		marker, markerColour = runGlyph(card.Active.State), m.palette.RunState(card.Active.State)
	case needsAnswer:
		marker, markerColour = needsAnswerGlyph, m.palette.NeedsAnswer
	default:
		// latestRun sorts a copy, so it stays behind both cases above.
		if latest := latestRun(card.Runs); latest != nil {
			marker, markerColour = runGlyph(latest.State), m.palette.Dim
		}
	}

	var head []string
	id := cardID(card.Spec)
	if kind := cardKind(card.Spec); kind != "" {
		id += " " + paint(m.cardKindColour(kind), "("+kind+")") + m.palette.Dim
	}
	head = append(head, id)
	if card.Spec.Claimed() {
		head = append(head, "@"+card.Spec.Owner)
	}
	// Only a source-backed card shows its age: a VirtualBoard spec also
	// carries created/updated, and its board never showed one.
	if sourceKind(card.Spec) != "" {
		if age, ok := specAge(card.Spec, time.Now()); ok {
			head = append(head, shortAge(age))
		}
	}
	headLine := paint(markerColour, marker) + " " + paint(m.palette.Dim, strings.Join(head, " · "))
	if links := cardLinkedIssuesLabel(card.Spec); links != "" {
		headLine += "  " + paint(m.palette.Accent, links)
	}

	titleLine := "  " + truncate(card.Spec.Title, width-3)

	var chips []string
	if needsAnswer {
		chips = append(chips, paint(m.palette.NeedsAnswer, needsAnswerBadge))
	}
	if card.Spec.Priority != "" {
		chips = append(chips, paint(m.palette.Priority(card.Spec.Priority), card.Spec.Priority))
	}
	if card.Spec.Complexity != "" {
		chips = append(chips, paint(m.palette.Dim, card.Spec.Complexity))
	}
	if card.Active != nil {
		label := card.Active.Role + " " + shortDuration(card.Active.Duration())
		if card.Active.Worktree != nil {
			// A worktree run is editing a different directory from the one
			// the user is looking at; that is worth one character.
			label = "⑂ " + label
		}
		chips = append(chips, paint(m.palette.RunState(card.Active.State), label))
	} else if latest := latestRun(card.Runs); latest != nil && latest.PullRequest != nil && latest.PullRequest.Opened {
		chips = append(chips, paint(m.palette.Succeeded, "PR"))
	} else if criteria := card.Spec.AcceptanceCriteria(); len(criteria) > 0 {
		done := 0
		for _, criterion := range criteria {
			if criterion.Done {
				done++
			}
		}
		chips = append(chips, paint(m.palette.Dim, fmt.Sprintf("%d/%d", done, len(criteria))))
	}
	chips = append(chips, m.sourceMeta(card)...)
	for _, label := range visibleLabels(card.Spec) {
		// The badge already said this one, in a word instead of a token.
		if needsAnswer && labelIsHITL(label) {
			continue
		}
		chips = append(chips, paint(m.palette.Dim, label))
	}
	chipLine := "  " + strings.Join(chips, "  ")

	lines := []string{fit(headLine, width), fit(titleLine, width), fit(chipLine, width)}
	if selected {
		for index, line := range lines {
			lines[index] = paint(m.palette.Invert, line)
		}
	}
	// A blank row between cards, at the same width so the column's separator
	// lands in the same place on every line.
	return append(lines, strings.Repeat(" ", width))
}

// splitWidth divides total across count columns, giving the remainder to the
// leftmost columns so the whole width is used exactly.
func splitWidth(total, count int) []int {
	base := total / count
	extra := total % count
	widths := make([]int, count)
	for index := range widths {
		widths[index] = base
		if index < extra {
			widths[index]++
		}
	}
	return widths
}

// scrollWindow returns the first index to draw so selected stays visible.
func scrollWindow(selected, total, visible int) int {
	if visible <= 0 || total <= visible {
		return 0
	}
	start := selected - visible/2
	if start < 0 {
		start = 0
	}
	if start > total-visible {
		start = total - visible
	}
	return start
}

func runGlyph(state runs.State) string {
	switch state {
	case runs.Running:
		return "▶"
	case runs.Queued:
		return "·"
	case runs.Awaiting:
		return "?"
	case runs.Succeeded:
		return "✓"
	case runs.Failed:
		return "✗"
	case runs.Cancelled:
		return "⊘"
	default:
		return " "
	}
}

func latestRun(list []*runs.Run) *runs.Run {
	sorted := sortRunsNewestFirst(list)
	if len(sorted) == 0 {
		return nil
	}
	return sorted[0]
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// shortAge is shortDuration for how long ago a card changed: a run lasts
// minutes, a pull request sits for weeks, and "1128h" tells nobody anything.
func shortAge(d time.Duration) string {
	if d < 24*time.Hour {
		return shortDuration(d)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
