package tui

import (
	"bufio"
	"os"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// Mouse reporting.
//
// The board asks the terminal for normal tracking (mode 1000) with SGR
// coordinates (mode 1006), and for nothing else. That combination is the whole
// design:
//
//   - 1000 reports a press, a release and a wheel notch. It does NOT report
//     motion, so the board never has to read — or ignore — a drag event per
//     cell the pointer crosses. There is no gesture here that needs one.
//   - 1006 encodes the report as `ESC [ < b ; x ; y M` in decimal text, so a
//     click past column 223 is representable and the payload can never be
//     mistaken for a keystroke. The legacy X10 encoding puts raw bytes in the
//     input stream, where a click on column 81 arrives as the letter `q` —
//     which this board binds to quit. See readX10Mouse for what happens to
//     one anyway.
//
// Enabling mouse reporting takes the terminal's own text selection away, which
// is a real loss for anyone who copies a card id out of the board. HVB_NO_MOUSE
// turns it off, the same shape as the NO_COLOR opt-out in NewPalette.
const (
	enableMouse  = "\x1b[?1000h\x1b[?1006h"
	disableMouse = "\x1b[?1006l\x1b[?1000l"
)

// mouseEnabled reports whether the board should ask for mouse reporting.
//
// Any non-empty value is an opt-out, per the NO_COLOR convention the palette
// already follows: an operator who exported the variable meant it, and making
// them spell `1` would be a second rule to remember.
func mouseEnabled() bool { return os.Getenv("HVB_NO_MOUSE") == "" }

// MouseKind is what the pointer did.
type MouseKind uint8

// The reports the board acts on. Release and drag are decoded rather than
// dropped because the bytes have to be consumed either way: an unparsed `m`
// report would be re-read as text.
const (
	MouseNone MouseKind = iota
	MousePress
	MouseRelease
	MouseDrag
	MouseScrollUp
	MouseScrollDown
	MouseScrollLeft
	MouseScrollRight
)

// MouseButton is which button a press or release carries.
type MouseButton uint8

const (
	MouseNoButton MouseButton = iota
	MouseLeft
	MouseMiddle
	MouseRight
)

// Mouse is one decoded report. X and Y are zero-based cell coordinates into
// the frame Model.Render produced, so Y indexes that slice directly.
//
// Modifier bits (shift, meta, ctrl) are deliberately dropped rather than kept
// in a field: no gesture on this board is bound to a modified click, and a
// field nothing reads is a field that goes stale. They are masked off so that
// a ctrl-click still reads as a left press instead of as a different button.
type Mouse struct {
	Kind   MouseKind
	Button MouseButton
	X      int
	Y      int
}

// SGR button-code bits, from the xterm control sequence reference.
const (
	sgrButtonMask   = 0x03 // which button, or 3 for none
	sgrMotionBit    = 0x20 // the report is a drag, not a click
	sgrWheelBit     = 0x40 // the low bits name a wheel direction, not a button
	sgrWheelUp      = 0
	sgrWheelDown    = 1
	sgrWheelLeft    = 2
	sgrWheelRight   = 3
	sgrMaxBodyBytes = 24 // `1048;9999;9999M` and room to spare
)

// readSGRMouse reads the rest of an SGR mouse report, the `ESC [ <` having
// already been consumed, and hands the body to parseSGRMouse.
//
// It blocks for the remaining bytes. That is safe here and nowhere else in the
// decoder: `ESC [ <` is not the prefix of any other input sequence, so the
// terminal is committed to finishing the report, however many writes it takes
// to arrive. A malformed body is reported as an unknown key rather than
// swallowed, so a desynchronised terminal shows up as a keystroke that does
// nothing instead of as a click somewhere the user did not point.
func readSGRMouse(reader *bufio.Reader) (Key, bool) {
	body := make([]byte, 0, 16)
	for {
		next, err := reader.ReadByte()
		if err != nil {
			return Key{Name: KeyUnknown}, true
		}
		body = append(body, next)
		if next == 'M' || next == 'm' {
			break
		}
		if len(body) >= sgrMaxBodyBytes {
			return Key{Name: KeyUnknown}, true
		}
	}
	event, ok := parseSGRMouse(body)
	if !ok {
		return Key{Name: KeyUnknown}, true
	}
	return Key{Name: KeyMouse, Mouse: event}, true
}

// readX10Mouse consumes a legacy X10 mouse report and reports nothing.
//
// The board asks for SGR coordinates (mode 1006), so a terminal only answers
// in X10 if it refused that request — which no terminal this board is used
// from does; SGR has been in xterm since 2012 and is in ghostty, kitty,
// WezTerm, iTerm2, Terminal.app, tmux and Windows Terminal. Supporting the
// encoding would buy nothing and cost a second parse path whose coordinates
// stop at column 223.
//
// The three payload bytes are consumed all the same, and that is the point of
// this function. They are raw bytes offset by 32, so a click on column 81
// carries the byte `q` — which this board binds to quit — and a click on
// column 82 carries `r`, a reload. Leaving them in the stream would turn an
// unreported click into a keystroke the user never typed.
func readX10Mouse(reader *bufio.Reader) (Key, bool) {
	for range 3 {
		if _, err := reader.ReadByte(); err != nil {
			break
		}
	}
	return Key{Name: KeyUnknown}, true
}

// parseSGRMouse decodes the body of an SGR mouse report: everything after the
// `ESC [ <` introducer, up to and including the final `M` or `m`.
//
// It is pure and total — every malformed input returns false rather than
// panicking — so the whole format is testable without a terminal.
func parseSGRMouse(body []byte) (Mouse, bool) {
	if len(body) == 0 {
		return Mouse{}, false
	}
	final := body[len(body)-1]
	if final != 'M' && final != 'm' {
		return Mouse{}, false
	}
	code, x, y, ok := threeNumbers(body[:len(body)-1])
	if !ok {
		return Mouse{}, false
	}
	// SGR coordinates are one-based; the frame is not.
	if x < 1 || y < 1 {
		return Mouse{}, false
	}
	event := Mouse{X: x - 1, Y: y - 1}
	switch {
	case code&sgrWheelBit != 0:
		switch code & sgrButtonMask {
		case sgrWheelUp:
			event.Kind = MouseScrollUp
		case sgrWheelDown:
			event.Kind = MouseScrollDown
		case sgrWheelLeft:
			event.Kind = MouseScrollLeft
		default:
			event.Kind = MouseScrollRight
		}
	case code&sgrMotionBit != 0:
		event.Kind, event.Button = MouseDrag, button(code)
	case final == 'M':
		event.Kind, event.Button = MousePress, button(code)
	default:
		event.Kind, event.Button = MouseRelease, button(code)
	}
	return event, true
}

func button(code int) MouseButton {
	switch code & sgrButtonMask {
	case 0:
		return MouseLeft
	case 1:
		return MouseMiddle
	case 2:
		return MouseRight
	default:
		return MouseNoButton
	}
}

// threeNumbers parses exactly `a;b;c` of decimal digits. It rejects an empty
// field, a stray separator and anything that is not a digit, so a truncated
// report is a refusal rather than a coordinate of zero.
func threeNumbers(text []byte) (a, b, c int, ok bool) {
	out := [3]int{}
	field, digits := 0, 0
	for _, ch := range text {
		switch {
		case ch >= '0' && ch <= '9':
			if out[field] > 1<<20 {
				return 0, 0, 0, false
			}
			out[field] = out[field]*10 + int(ch-'0')
			digits++
		case ch == ';':
			if digits == 0 || field == 2 {
				return 0, 0, 0, false
			}
			field, digits = field+1, 0
		default:
			return 0, 0, 0, false
		}
	}
	if field != 2 || digits == 0 {
		return 0, 0, 0, false
	}
	return out[0], out[1], out[2], true
}

// mouseTarget is the board cell under the pointer: which column, and which of
// its cards, if any.
type mouseTarget struct {
	column int
	status feature.Status
	// card indexes the column's cards, or is -1 when the row under the
	// pointer is the column's title, its rule, its auto line, the dash of an
	// empty column, or the blank space below the last card.
	card int
}

// locate maps a frame cell to a board target.
//
// The arithmetic here is renderColumn's, reached through the same helpers it
// uses — columnOrder for which columns exist, splitWidth for their widths,
// scrollWindow for the first visible card, cardHeight for the block size — and
// not a second copy of them. Two copies of a layout diverge, and the symptom
// of a divergence is a click that focuses the wrong card with no error
// anywhere. The one fact not available as a helper is how many rows
// renderColumn puts above its first card; columnPrefixRows names it, and
// TestMouseHitTestAgreesWithTheRenderedFrame pins it against Render's own
// output so a change there fails a test instead of mis-focusing a card.
func (m *Model) locate(x, y int) (mouseTarget, bool) {
	order := m.columnOrder()
	if len(order) == 0 || x < 0 || x >= m.width {
		return mouseTarget{}, false
	}
	// Model.frame puts the header on the first row and the footer on the
	// last, with the body between them.
	row, height := y-1, m.height-2
	if row < 0 || row >= height {
		return mouseTarget{}, false
	}

	if m.compact() {
		// renderSingleColumn spends the first body row on the lifecycle
		// breadcrumb and draws the one visible column below it, a row
		// shorter. The breadcrumb is not a click target: its segments are
		// laid out in renderColumns' sibling, and a second copy of that
		// arithmetic would be exactly the divergence this comment warns
		// about. Changing column on a narrow terminal stays on h/l.
		if row == 0 || m.column < 0 || m.column >= len(order) {
			return mouseTarget{}, false
		}
		status := order[m.column]
		return mouseTarget{
			column: m.column,
			status: status,
			card:   m.cardAtRow(status, row-1, height-1),
		}, true
	}

	widths := splitWidth(m.width, len(order))
	left, index := 0, -1
	for candidate, width := range widths {
		if x < left+width {
			index = candidate
			break
		}
		left += width
	}
	if index < 0 {
		return mouseTarget{}, false
	}
	status := order[index]
	return mouseTarget{column: index, status: status, card: m.cardAtRow(status, row, height)}, true
}

// columnPrefixRows is how many rows renderColumn draws above its first card:
// the title and the rule under it, plus the auto-routing line a configured
// column adds.
func columnPrefixRows(auto bool) int {
	if auto {
		return 3
	}
	return 2
}

// cardAtRow maps a row inside one rendered column to the card drawn there, or
// -1 when no card is.
func (m *Model) cardAtRow(status feature.Status, row, height int) int {
	cards := m.Cards(status)
	if len(cards) == 0 || row < 0 {
		return -1
	}
	prefix := columnPrefixRows(m.backend.Config().Column(status).Auto)
	if row < prefix {
		return -1
	}
	visible := height - prefix
	if visible <= 0 {
		return -1
	}
	start := scrollWindow(m.card[status], len(cards), visible/cardHeight)
	index := start + (row-prefix)/cardHeight
	if index < 0 || index >= len(cards) {
		return -1
	}
	// renderColumn stops before a card whose block would not fit, so the
	// rows a partial card would have occupied hold nothing.
	if prefix+(index-start+1)*cardHeight > height {
		return -1
	}
	return index
}

// handleMouse applies one mouse report. It is the only thing update.go's
// Handle dispatches for a mouse event, so every gesture the board answers is
// in this one function.
//
// The gestures, and what they are bound to:
//
//   - left press on a card focuses its column and that card — the mouse
//     equivalent of h/l/j/k;
//   - left press on the same card again opens its detail, which is what enter
//     does;
//   - a wheel notch focuses the column under the pointer and moves its card
//     selection, which is what j/k do;
//   - a horizontal wheel notch moves the column selection, which is what h/l
//     do.
//
// Deliberately NOT bound:
//
//   - Double click. Telling one from two clicks needs the time and place of
//     the previous click kept somewhere between events, and the only place
//     that could live is the Model. Without it a double click is two clicks,
//     and a guess based on anything else would be a timer this event loop does
//     not have. Clicking an already-focused card opens the detail instead:
//     same two clicks, no stored state, and no wrong answer when the second
//     click is slow.
//   - Middle and right press, and drag. The board has no verb for any of
//     them, and binding one to a verb it does have — moving a card by
//     dragging it, say — would be a lifecycle transition triggered by an
//     accident of the wrist.
func (m *Model) handleMouse(event Mouse) bool {
	if m.view == ViewDetail {
		// The detail view is one scrolling page. It has a scroll verb and
		// nothing a cell maps to.
		switch event.Kind {
		case MouseScrollUp:
			if m.detailScroll == 0 {
				return false
			}
			m.detailScroll--
			return true
		case MouseScrollDown:
			m.detailScroll++
			return true
		}
		return false
	}
	if m.view != ViewBoard {
		// An overlay is drawn on top of the board, so the cell under the
		// pointer belongs to a row the user cannot see. There is no honest
		// target: the pickers, the form and the confirmation stay on the
		// keyboard.
		return false
	}

	target, ok := m.locate(event.X, event.Y)
	if !ok {
		return false
	}
	switch event.Kind {
	case MousePress:
		if event.Button != MouseLeft {
			return false
		}
		return m.clickBoard(target)
	case MouseScrollUp:
		return m.scrollBoard(target, -1)
	case MouseScrollDown:
		return m.scrollBoard(target, 1)
	case MouseScrollLeft:
		m.moveColumn(-1)
		return true
	case MouseScrollRight:
		m.moveColumn(1)
		return true
	}
	return false
}

func (m *Model) clickBoard(target mouseTarget) bool {
	// Read before moving anything: the second click on a card is the first
	// click's own result seen again.
	reclick := target.card >= 0 && m.column == target.column && m.card[target.status] == target.card
	m.column = target.column
	if target.card >= 0 {
		m.card[target.status] = target.card
	}
	// A click on a column's empty space focuses the column and leaves its
	// card selection alone: there is no card under the pointer to mean.
	if reclick && m.FocusedCard() != nil {
		m.view, m.detailScroll = ViewDetail, 0
	}
	return true
}

func (m *Model) scrollBoard(target mouseTarget, delta int) bool {
	// The selection highlight is drawn only in the focused column, so moving
	// another column's card selection would be a change with nothing on
	// screen to show it. The wheel focuses the column it is over first.
	m.column = target.column
	m.moveCard(delta)
	return true
}
