// Package tui renders the kanban board.
//
// The board is drawn by hand with ANSI escapes rather than through a TUI
// framework. It buys two things worth having in a plugin: the binary stays
// small and dependency-light, and every view is a pure function from state to
// []string, so the layout is unit-testable without a terminal.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Screen owns the terminal: raw mode, the alternate buffer, and a buffered
// writer that flushes one complete frame at a time so the board never tears.
type Screen struct {
	in      *os.File
	out     *bufio.Writer
	restore *term.State
	width   int
	height  int
	rawMode bool
	altebuf bool
	// mouse records that the terminal was asked to report clicks, so Close
	// can tell "asked and must unask" from "never asked" — including on the
	// second Close, and on a board the operator opted out of.
	mouse    bool
	lastDraw []string
}

// Minimum usable geometry. Below this the board cannot show a readable column,
// so it renders a single message instead of a broken layout.
const (
	minWidth  = 30
	minHeight = 10
)

// NewScreen puts the terminal into raw mode on the alternate buffer.
//
// reportMouse asks the terminal to report clicks, and is a parameter rather
// than a property of every screen because reporting takes the terminal's own
// text selection away for as long as it is up. The board wants that trade: it
// binds clicks and the scroll wheel. RunNotice does not — its only verb is
// "press a key", and a notice is exactly the thing a user wants to copy the
// `vb init` line out of, so it asks for no reporting and keeps selection.
// HVB_NO_MOUSE still overrides a true, for the operator who wants selection
// back on the board too.
func NewScreen(reportMouse bool) (*Screen, error) {
	in := os.Stdin
	screen := &Screen{in: in, out: bufio.NewWriterSize(os.Stdout, 1<<16)}
	if !term.IsTerminal(int(in.Fd())) {
		return nil, fmt.Errorf("hvb tui needs a terminal on stdin")
	}
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("enter raw mode: %w", err)
	}
	screen.restore, screen.rawMode = state, true
	screen.Size()
	screen.write(enterAltScreen + hideCursor + clearScreen)
	screen.altebuf = true
	if reportMouse && mouseEnabled() {
		screen.write(enableMouse)
		screen.mouse = true
	}
	return screen, screen.out.Flush()
}

// Close restores the terminal. It is safe to call twice, which matters because
// both the normal exit path and a panic recovery call it.
//
// Mouse reporting is turned off here and only here, for the same reason raw
// mode is: this is the one restore path. Run defers Close immediately after
// NewScreen, so a normal quit, a cancelled context, a Draw that errors and a
// panic unwinding through a render all reach it — and the board never
// suspends itself to run something else, so there is no second place that has
// to hand the terminal back. A process that exits with mode 1000 still on
// leaves the user's shell printing `<35;61;12M` every time they move the
// pointer, which is why this runs before the alternate buffer is released and
// is flushed on its own.
func (s *Screen) Close() {
	if s == nil {
		return
	}
	if s.mouse {
		s.write(disableMouse)
		_ = s.out.Flush()
		s.mouse = false
	}
	if s.altebuf {
		s.write(showCursor + exitAltScreen)
		_ = s.out.Flush()
		s.altebuf = false
	}
	if s.rawMode && s.restore != nil {
		_ = term.Restore(int(s.in.Fd()), s.restore)
		s.rawMode = false
	}
}

// Size refreshes and returns the terminal geometry.
func (s *Screen) Size() (width, height int) {
	w, h, err := term.GetSize(int(s.in.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	s.width, s.height = w, h
	return w, h
}

// Draw paints a frame.
//
// Only lines that changed since the previous frame are repainted. A board
// refresh that moves one card should not repaint 50 rows over an ssh link, and
// full repaints are what make a hand-rolled TUI flicker.
func (s *Screen) Draw(lines []string) error {
	height := s.height
	if len(lines) > height {
		lines = lines[:height]
	}
	var frame strings.Builder
	for index := 0; index < height; index++ {
		line := ""
		if index < len(lines) {
			line = lines[index]
		}
		if index < len(s.lastDraw) && s.lastDraw[index] == line {
			continue
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H%s%s", index+1, clearLine, line)
	}
	s.lastDraw = append(s.lastDraw[:0], lines...)
	for len(s.lastDraw) < height {
		s.lastDraw = append(s.lastDraw, "")
	}
	s.write(frame.String())
	return s.out.Flush()
}

// Invalidate forces the next Draw to repaint every line, which is what a resize
// or a returning-from-a-subprocess redraw needs.
func (s *Screen) Invalidate() {
	s.lastDraw = nil
	s.write(clearScreen)
	_ = s.out.Flush()
}

// Input returns the reader for key events.
func (s *Screen) Input() io.Reader { return s.in }

func (s *Screen) write(text string) { _, _ = s.out.WriteString(text) }

// ANSI control sequences.
const (
	enterAltScreen = "\x1b[?1049h"
	exitAltScreen  = "\x1b[?1049l"
	hideCursor     = "\x1b[?25l"
	showCursor     = "\x1b[?25h"
	clearScreen    = "\x1b[2J\x1b[H"
	clearLine      = "\x1b[2K"
)
