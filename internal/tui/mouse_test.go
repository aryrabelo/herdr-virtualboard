package tui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

func TestParseSGRMouseDecodesEveryReportTheBoardAsksFor(t *testing.T) {
	cases := []struct {
		body string
		want Mouse
	}{
		// SGR coordinates are one-based; the frame is not.
		{"0;1;1M", Mouse{Kind: MousePress, Button: MouseLeft, X: 0, Y: 0}},
		{"0;61;12M", Mouse{Kind: MousePress, Button: MouseLeft, X: 60, Y: 11}},
		{"1;61;12M", Mouse{Kind: MousePress, Button: MouseMiddle, X: 60, Y: 11}},
		{"2;61;12M", Mouse{Kind: MousePress, Button: MouseRight, X: 60, Y: 11}},
		{"0;61;12m", Mouse{Kind: MouseRelease, Button: MouseLeft, X: 60, Y: 11}},
		{"64;61;12M", Mouse{Kind: MouseScrollUp, X: 60, Y: 11}},
		{"65;61;12M", Mouse{Kind: MouseScrollDown, X: 60, Y: 11}},
		{"66;61;12M", Mouse{Kind: MouseScrollLeft, X: 60, Y: 11}},
		{"67;61;12M", Mouse{Kind: MouseScrollRight, X: 60, Y: 11}},
		// A modified click is still a click by the same button: ctrl is bit
		// 4, shift bit 2, meta bit 3, and none of them names a button.
		{"16;61;12M", Mouse{Kind: MousePress, Button: MouseLeft, X: 60, Y: 11}},
		{"28;61;12M", Mouse{Kind: MousePress, Button: MouseLeft, X: 60, Y: 11}},
		// Ctrl-wheel is still a wheel, which is how a terminal reports a
		// zoom gesture the board has no verb for.
		{"80;61;12M", Mouse{Kind: MouseScrollUp, X: 60, Y: 11}},
		// Motion (bit 5) is a drag, not a press. The board never asks for
		// motion reports, but a terminal that sends one must not have it
		// read as a click.
		{"32;61;12M", Mouse{Kind: MouseDrag, Button: MouseLeft, X: 60, Y: 11}},
		{"35;61;12M", Mouse{Kind: MouseDrag, Button: MouseNoButton, X: 60, Y: 11}},
		// A wide terminal past the 223-column ceiling of the legacy
		// encoding, which is the whole reason for asking for SGR.
		{"0;400;300M", Mouse{Kind: MousePress, Button: MouseLeft, X: 399, Y: 299}},
	}
	for _, test := range cases {
		got, ok := parseSGRMouse([]byte(test.body))
		if !ok {
			t.Errorf("parseSGRMouse(%q) refused a well-formed report", test.body)
			continue
		}
		if got != test.want {
			t.Errorf("parseSGRMouse(%q) = %+v, want %+v", test.body, got, test.want)
		}
	}
}

func TestParseSGRMouseRefusesAMalformedReport(t *testing.T) {
	// Every one of these is a refusal rather than a coordinate of zero: a
	// click the decoder is not sure about must do nothing, not move the
	// selection to the top-left card.
	for _, body := range []string{
		"",
		"0;61;12",    // truncated before the final byte
		"M",          // final byte with no fields
		"0;61M",      // two fields
		"0;61;12;3M", // four fields
		"0;6a;12M",   // not a number
		";61;12M",    // empty first field
		"0;;12M",     // empty middle field
		"0;61;M",     // empty last field
		"0;0;12M",    // column zero: SGR counts from one
		"0;61;0M",    // row zero
		"0;61;12X",   // not a mouse final byte
		"99999999999999999999;61;12M",
	} {
		if event, ok := parseSGRMouse([]byte(body)); ok {
			t.Errorf("parseSGRMouse(%q) accepted a malformed report as %+v", body, event)
		}
	}
}

// chunkReader hands out its bytes in exactly the pieces it was given, so a
// test can put a split anywhere inside an escape sequence. A terminal does not
// promise that a report arrives in one read.
type chunkReader struct{ chunks [][]byte }

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.chunks) > 0 && len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
	}
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	count := copy(p, c.chunks[0])
	c.chunks[0] = c.chunks[0][count:]
	return count, nil
}

// drainKeys reads every event ReadKeys produces for r and fails if the decoder
// blocks instead of finishing. A decoder that waits forever for the rest of a
// sequence would freeze the board's input, which no malformed byte may do.
func drainKeys(t *testing.T, r io.Reader) []Key {
	t.Helper()
	keys := ReadKeys(r)
	out := []Key{}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case key, ok := <-keys:
			if !ok {
				return out
			}
			out = append(out, key)
		case <-deadline:
			t.Fatalf("ReadKeys is blocked after %d events", len(out))
			return out
		}
	}
}

func TestReadKeysDeliversAMouseReportAsAnInputEvent(t *testing.T) {
	got := drainKeys(t, strings.NewReader("\x1b[<0;61;12M\x1b[<0;61;12m\x1b[<65;3;4M"))
	want := []Key{
		{Name: KeyMouse, Mouse: Mouse{Kind: MousePress, Button: MouseLeft, X: 60, Y: 11}},
		{Name: KeyMouse, Mouse: Mouse{Kind: MouseRelease, Button: MouseLeft, X: 60, Y: 11}},
		{Name: KeyMouse, Mouse: Mouse{Kind: MouseScrollDown, X: 2, Y: 3}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("event %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestReadKeysSurvivesATruncatedMouseReport(t *testing.T) {
	// The stream ends mid-report. The decoder must give up on it, not wait
	// for bytes that are never coming, and must not report a click.
	for _, input := range []string{"\x1b[<", "\x1b[<0", "\x1b[<0;61", "\x1b[<0;61;12"} {
		for _, key := range drainKeys(t, strings.NewReader(input)) {
			if key.Name == KeyMouse {
				t.Errorf("%q produced a click: %+v", input, key.Mouse)
			}
		}
	}
}

func TestReadKeysRefusesAnInvalidMouseReportWithoutLeakingItsBytes(t *testing.T) {
	// A body the parser rejects must not be re-read as text. `6a` is not a
	// number, and `a` is not a key the user pressed.
	got := drainKeys(t, strings.NewReader("\x1b[<0;6a;12Mq"))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (the refused report and the q): %+v", len(got), got)
	}
	if got[0].Name != KeyUnknown {
		t.Errorf("a malformed report must decode as an unknown key, got %+v", got[0])
	}
	if got[1].Rune != 'q' {
		t.Errorf("the byte after the report must survive, got %+v", got[1])
	}
}

func TestReadKeysDecodesAMouseReportArrivingInPieces(t *testing.T) {
	// A report is split at every byte boundary in turn. From the third byte
	// on — once `ESC [ <` has arrived, which can only be a mouse report —
	// the decoder is committed and blocks for the rest, so the event is
	// decoded whole.
	//
	// A split after the bare ESC is the one case that cannot be decoded: an
	// ESC with nothing behind it is a genuine Escape press, and telling the
	// two apart needs a timer this decoder deliberately does not have (see
	// ReadKeys). It must still not hang or panic, which is what the drain
	// below checks for every split.
	const report = "\x1b[<0;61;12M"
	want := Mouse{Kind: MousePress, Button: MouseLeft, X: 60, Y: 11}
	for split := 1; split < len(report); split++ {
		reader := &chunkReader{chunks: [][]byte{[]byte(report[:split]), []byte(report[split:])}}
		got := drainKeys(t, reader)
		clicks := []Mouse{}
		for _, key := range got {
			if key.Name == KeyMouse {
				clicks = append(clicks, key.Mouse)
			}
		}
		if split == 1 {
			if len(clicks) != 0 {
				t.Errorf("split after the bare ESC: expected no click, got %+v", clicks)
			}
			continue
		}
		if len(clicks) != 1 || clicks[0] != want {
			t.Errorf("split at %d: got %+v, want exactly one %+v (all events: %+v)",
				split, clicks, want, got)
		}
	}
}

func TestReadKeysSwallowsALegacyX10ReportInsteadOfReadingItAsKeys(t *testing.T) {
	// X10 encodes the report as raw bytes offset by 32, so a click on column
	// 81 carries `q` — quit — and one on column 82 carries `r`. The board
	// never asks for that encoding, but if a terminal answers in it anyway
	// the payload must be consumed rather than left to be read as text.
	got := drainKeys(t, strings.NewReader("\x1b[M\x20\x71\x72"))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (the report itself, acted on by nothing): %+v", len(got), got)
	}
	if got[0].Name != KeyUnknown {
		t.Errorf("an X10 report must decode as an unknown key, got %+v", got[0])
	}
	for _, key := range got {
		if key.Rune == 'q' || key.Rune == 'r' {
			t.Fatalf("an X10 payload byte leaked into the board as %q", key.Rune)
		}
	}
}

// cardCell finds the cell a card's title is drawn in by searching the frame
// Render produced. It deliberately re-derives no layout: a hit-test asserted
// against arithmetic copied from the hit-test passes with the arithmetic
// wrong, which is the one failure this whole file exists to catch.
func cardCell(t *testing.T, model *Model, title string) (x, y int) {
	t.Helper()
	for row, line := range model.Render() {
		if index := strings.Index(line, title); index >= 0 {
			return index, row
		}
	}
	t.Fatalf("card %q is not on the board:\n%s", title, screen(model))
	return 0, 0
}

func click(x, y int) Key {
	return Key{Name: KeyMouse, Mouse: Mouse{Kind: MousePress, Button: MouseLeft, X: x, Y: y}}
}

func wheel(kind MouseKind, x, y int) Key {
	return Key{Name: KeyMouse, Mouse: Mouse{Kind: kind, X: x, Y: y}}
}

func boardWithCards(t *testing.T) *Model {
	t.Helper()
	return newTestModel(t, newFakeBackend(
		spec("FTR-0001", "Alfa thing", feature.Backlog),
		spec("FTR-0002", "Bravo thing", feature.Backlog),
		spec("FTR-0003", "Charlie thing", feature.Review),
		spec("FTR-0004", "Delta thing", feature.Review),
		spec("FTR-0005", "Echo thing", feature.Review),
	), 150, 30)
}

func TestMouseClickFocusesTheColumnAndCardUnderThePointer(t *testing.T) {
	model := boardWithCards(t)
	if model.FocusedStatus() != feature.Backlog {
		t.Fatalf("the board should start on the first populated column, got %s", model.FocusedStatus())
	}

	x, y := cardCell(t, model, "Delta thing")
	if !model.Handle(context.Background(), click(x, y)) {
		t.Fatal("a click on a card must redraw the board")
	}
	if got := model.FocusedStatus(); got != feature.Review {
		t.Errorf("clicking a Review card focused %s", got)
	}
	card := model.FocusedCard()
	if card == nil || card.Spec.ID != "FTR-0004" {
		t.Fatalf("clicking Delta focused %v", card)
	}
	if model.view != ViewBoard {
		t.Errorf("the first click must focus, not open the detail")
	}

	// And back, to a card that is not the first in its column.
	x, y = cardCell(t, model, "Bravo thing")
	model.Handle(context.Background(), click(x, y))
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0002" {
		t.Fatalf("clicking Bravo focused %v", card)
	}
}

func TestMouseClickOnTheFocusedCardOpensItsDetail(t *testing.T) {
	model := boardWithCards(t)
	x, y := cardCell(t, model, "Charlie thing")
	model.Handle(context.Background(), click(x, y))
	if model.view != ViewBoard {
		t.Fatal("the first click must not open the detail")
	}
	model.Handle(context.Background(), click(x, y))
	if model.view != ViewDetail {
		t.Fatalf("a second click on the focused card must open its detail, view is %v", model.view)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0003" {
		t.Fatalf("the detail opened on %v", card)
	}
}

func TestMouseClickOnAColumnsEmptySpaceFocusesItAndKeepsItsCard(t *testing.T) {
	model := boardWithCards(t)
	// Focus the third Review card, then click the column's title row: the
	// column is the target, and there is no card under the pointer to mean.
	x, y := cardCell(t, model, "Echo thing")
	model.Handle(context.Background(), click(x, y))
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0005" {
		t.Fatalf("setup: focus is %v", card)
	}

	backlogX, backlogY := cardCell(t, model, "Alfa thing")
	model.Handle(context.Background(), click(backlogX, backlogY))
	if model.FocusedStatus() != feature.Backlog {
		t.Fatalf("setup: focus is %s", model.FocusedStatus())
	}

	// The title row of the Review column, located in the frame rather than
	// computed: the first row below the header that carries it.
	titleX, titleY := -1, -1
	for row, line := range model.Render() {
		if row == 0 {
			continue // the header names every column too
		}
		if index := strings.Index(line, "REVIEW"); index >= 0 {
			titleX, titleY = index, row
			break
		}
	}
	if titleY < 0 {
		t.Fatalf("no Review column title on the board:\n%s", screen(model))
	}
	model.Handle(context.Background(), click(titleX, titleY))
	if got := model.FocusedStatus(); got != feature.Review {
		t.Errorf("clicking the Review title focused %s", got)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0005" {
		t.Errorf("clicking a column's empty space must keep its card selection, got %v", card)
	}
}

func TestMouseWheelMovesTheCardSelectionInTheColumnUnderThePointer(t *testing.T) {
	model := boardWithCards(t)
	x, y := cardCell(t, model, "Charlie thing")

	if !model.Handle(context.Background(), wheel(MouseScrollDown, x, y)) {
		t.Fatal("a wheel notch must redraw the board")
	}
	if got := model.FocusedStatus(); got != feature.Review {
		t.Fatalf("the wheel must focus the column under the pointer, focused %s", got)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0004" {
		t.Fatalf("one notch down from Charlie focused %v", card)
	}
	model.Handle(context.Background(), wheel(MouseScrollDown, x, y))
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0005" {
		t.Fatalf("two notches down focused %v", card)
	}
	// The end of the column is the end: the selection clamps rather than
	// wrapping round to the top.
	model.Handle(context.Background(), wheel(MouseScrollDown, x, y))
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0005" {
		t.Fatalf("a notch past the last card focused %v", card)
	}
	model.Handle(context.Background(), wheel(MouseScrollUp, x, y))
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "FTR-0004" {
		t.Fatalf("one notch up focused %v", card)
	}
	// The column it was over is the one that moved.
	if model.card[feature.Backlog] != 0 {
		t.Errorf("the wheel moved a column it was not over: backlog is on card %d",
			model.card[feature.Backlog])
	}
}

func TestMouseWheelSidewaysMovesTheColumnSelection(t *testing.T) {
	model := boardWithCards(t)
	x, y := cardCell(t, model, "Alfa thing")
	order := model.columnOrder()

	model.Handle(context.Background(), wheel(MouseScrollRight, x, y))
	if got := model.FocusedStatus(); got != order[1] {
		t.Errorf("a notch right focused %s, want %s", got, order[1])
	}
	model.Handle(context.Background(), wheel(MouseScrollLeft, x, y))
	if got := model.FocusedStatus(); got != order[0] {
		t.Errorf("a notch left focused %s, want %s", got, order[0])
	}
	// The first column is the first: the selection clamps.
	model.Handle(context.Background(), wheel(MouseScrollLeft, x, y))
	if got := model.FocusedStatus(); got != order[0] {
		t.Errorf("a notch left of the first column focused %s", got)
	}
}

// TestMouseHitTestAgreesWithTheRenderedFrame is the guard on the one piece of
// layout arithmetic that had to be written twice: renderColumn decides where a
// card is drawn, and locate decides which card a cell belongs to. Both are
// built from the same helpers, but the number of rows renderColumn puts above
// its first card is a fact only renderColumn knows. This pins that fact
// against Render's own output, for every card and every one of its rows, so a
// change to the column's header rows fails here instead of silently focusing
// the wrong card.
func TestMouseHitTestAgreesWithTheRenderedFrame(t *testing.T) {
	specs := []*feature.Spec{}
	for index, title := range []string{"Alfa", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"} {
		status := feature.Backlog
		if index%2 == 1 {
			status = feature.Review
		}
		specs = append(specs, spec("FTR-000"+string(rune('1'+index)), title+" thing", status))
	}
	model := newTestModel(t, newFakeBackend(specs...), 150, 30)

	frame := model.Render()
	checked := 0
	// The row of the first card drawn in each column, so the rule above it
	// can be checked for holding no card. Without that the test passes with
	// every row shifted by one, since a shift of one keeps each of a card's
	// own three rows inside the same four-row block.
	firstCardRow := map[feature.Status]int{}
	for _, want := range specs {
		for row, line := range frame {
			if !strings.Contains(line, want.Title) {
				continue
			}
			if first, ok := firstCardRow[want.Status]; !ok || row < first {
				firstCardRow[want.Status] = row
			}
			break
		}
	}
	for _, want := range specs {
		titleRow := -1
		titleX := -1
		for row, line := range frame {
			if index := strings.Index(line, want.Title); index >= 0 {
				titleRow, titleX = row, index
				break
			}
		}
		if titleRow < 0 {
			continue // scrolled out of view; nothing on screen to check
		}
		// A card block is header, title, chips, spacer. The title is the
		// second row, so the first three rows of the block are these.
		for offset := -1; offset <= 1; offset++ {
			target, ok := model.locate(titleX, titleRow+offset)
			if !ok {
				t.Errorf("%s row %+d: locate refused a cell holding a card", want.ID, offset)
				continue
			}
			if target.status != want.Status {
				t.Errorf("%s row %+d: locate says column %s, the frame draws it in %s",
					want.ID, offset, target.status, want.Status)
				continue
			}
			cards := model.Cards(target.status)
			if target.card < 0 || target.card >= len(cards) {
				t.Errorf("%s row %+d: locate found no card where the frame drew one",
					want.ID, offset)
				continue
			}
			if got := cards[target.card].Spec.ID; got != want.ID {
				t.Errorf("%s row %+d: locate says %s", want.ID, offset, got)
			}
			checked++
		}
		// The rule under the column title is not a card, and the cell two
		// rows above the first card's title is that rule.
		if firstCardRow[want.Status] == titleRow {
			target, ok := model.locate(titleX, titleRow-2)
			if !ok {
				t.Errorf("%s: locate refused the rule row of its own column", want.ID)
			} else if target.card >= 0 {
				t.Errorf("%s: locate says the rule above the first card holds card %d",
					want.ID, target.card)
			}
			checked++
		}
	}
	if checked < 12 {
		t.Fatalf("only %d cells checked; the fixture is not exercising the hit test", checked)
	}
}

func TestMouseIsIgnoredOutsideTheBoard(t *testing.T) {
	model := boardWithCards(t)
	x, y := cardCell(t, model, "Alfa thing")
	for _, view := range []View{ViewHelp, ViewNewFeature, ViewMovePicker, ViewDispatchPicker, ViewConfirm} {
		model.view = view
		if model.Handle(context.Background(), click(x, y)) {
			t.Errorf("a click under an overlay (%v) must be ignored", view)
		}
		if model.view != view {
			t.Errorf("a click under an overlay (%v) changed the view to %v", view, model.view)
		}
	}
}

func TestMouseWheelScrollsTheDetailView(t *testing.T) {
	model := boardWithCards(t)
	model.view = ViewDetail
	if !model.Handle(context.Background(), wheel(MouseScrollDown, 4, 4)) {
		t.Fatal("a wheel notch in the detail view must redraw")
	}
	if model.detailScroll != 1 {
		t.Errorf("a notch down scrolled to %d, want 1", model.detailScroll)
	}
	model.Handle(context.Background(), wheel(MouseScrollUp, 4, 4))
	if model.detailScroll != 0 {
		t.Errorf("a notch up scrolled to %d, want 0", model.detailScroll)
	}
	// Already at the top: nothing changed, so nothing is redrawn.
	if model.Handle(context.Background(), wheel(MouseScrollUp, 4, 4)) {
		t.Error("a notch up at the top of the detail must not redraw")
	}
	if model.detailScroll != 0 {
		t.Errorf("the detail scrolled above its top, to %d", model.detailScroll)
	}
}

func TestMouseIgnoresGesturesTheBoardHasNoVerbFor(t *testing.T) {
	model := boardWithCards(t)
	x, y := cardCell(t, model, "Delta thing")
	before := model.FocusedStatus()
	for _, event := range []Mouse{
		{Kind: MousePress, Button: MouseRight, X: x, Y: y},
		{Kind: MousePress, Button: MouseMiddle, X: x, Y: y},
		{Kind: MouseRelease, Button: MouseLeft, X: x, Y: y},
		{Kind: MouseDrag, Button: MouseLeft, X: x, Y: y},
	} {
		if model.Handle(context.Background(), Key{Name: KeyMouse, Mouse: event}) {
			t.Errorf("%+v must be ignored", event)
		}
	}
	if model.FocusedStatus() != before {
		t.Errorf("an unbound gesture moved the focus to %s", model.FocusedStatus())
	}
}

func TestMouseOffTheBoardIsIgnored(t *testing.T) {
	model := boardWithCards(t)
	// The header row, the footer row, and past the right edge: Model.frame
	// owns the first and last rows, and nothing owns a cell off the side.
	for _, cell := range [][2]int{{4, 0}, {4, 29}, {4, 30}, {150, 4}, {-1, 4}, {4, -1}} {
		if model.Handle(context.Background(), click(cell[0], cell[1])) {
			t.Errorf("a click at %v is not on the board and must be ignored", cell)
		}
	}
}

func TestCloseTurnsMouseReportingOffOnTheRestorePath(t *testing.T) {
	// Run defers Screen.Close immediately after NewScreen, so this is the
	// single path a normal quit, a cancelled context, a failed Draw and a
	// panic all leave through.
	var out bytes.Buffer
	terminal := &Screen{out: bufio.NewWriter(&out), altebuf: true, mouse: true}
	terminal.Close()

	written := out.String()
	off := strings.Index(written, disableMouse)
	if off < 0 {
		t.Fatalf("Close did not turn mouse reporting off: %q", written)
	}
	if alt := strings.Index(written, exitAltScreen); off > alt {
		t.Errorf("mouse reporting must be turned off before the alternate buffer is released: %q", written)
	}

	// Close is called twice on some paths, and the second call must not
	// write a sequence to a terminal that is no longer ours.
	terminal.Close()
	if count := strings.Count(out.String(), disableMouse); count != 1 {
		t.Errorf("Close wrote the disable sequence %d times, want 1", count)
	}
}

func TestCloseLeavesMouseReportingAloneWhenItWasNeverAskedFor(t *testing.T) {
	// The observable consequence of the opt-out: a board that never enabled
	// reporting must not disable it either, because the sequence would also
	// turn off reporting that something else — a shell, an editor the user
	// suspended — had asked for.
	var out bytes.Buffer
	terminal := &Screen{out: bufio.NewWriter(&out), altebuf: true}
	terminal.Close()
	if strings.Contains(out.String(), disableMouse) {
		t.Errorf("Close disabled mouse reporting it never enabled: %q", out.String())
	}
}

func TestMouseReportingIsOptOutThroughTheEnvironment(t *testing.T) {
	// Anyone on ssh, or anyone who wants the terminal's own text selection
	// back, needs a way out: enabling mouse reporting takes selection away.
	if !mouseEnabled() {
		t.Fatal("mouse reporting must be on by default")
	}
	t.Setenv("HVB_NO_MOUSE", "1")
	if mouseEnabled() {
		t.Fatal("HVB_NO_MOUSE must turn mouse reporting off")
	}
	// Any value means it, per the NO_COLOR convention NewPalette follows.
	t.Setenv("HVB_NO_MOUSE", "0")
	if mouseEnabled() {
		t.Fatal("HVB_NO_MOUSE=0 is still an opt-out, like NO_COLOR")
	}
}

// TestMouseBytesFromTheTerminalMoveTheBoardsFocus is the whole path with only
// the terminal itself left out: the bytes a terminal writes for a click, read
// by ReadKeys, delivered on the channel the event loop already selects on, and
// handed to Handle exactly as the loop hands it a key press.
//
// Every other test in this file injects a decoded Mouse, so each of them would
// still pass with the decoder wired to nothing.
func TestMouseBytesFromTheTerminalMoveTheBoardsFocus(t *testing.T) {
	model := boardWithCards(t)
	x, y := cardCell(t, model, "Echo thing")

	// What a terminal writes for a left click there, and then for a wheel
	// notch up in the same cell. SGR counts rows and columns from one.
	input := fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm\x1b[<64;%d;%dM",
		x+1, y+1, x+1, y+1, x+1, y+1)

	for _, key := range drainKeys(t, strings.NewReader(input)) {
		model.Handle(context.Background(), key)
	}
	if got := model.FocusedStatus(); got != feature.Review {
		t.Errorf("clicking a Review card focused %s", got)
	}
	// Echo is the third Review card; the wheel notch up moved to the second.
	card := model.FocusedCard()
	if card == nil || card.Spec.ID != "FTR-0004" {
		t.Fatalf("click on Echo then one notch up focused %v", card)
	}
	if model.view != ViewBoard {
		t.Errorf("a click and a release are one click, not two: view is %v", model.view)
	}
}
