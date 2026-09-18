package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Notice is a full-screen message the board shows instead of a board.
type Notice struct {
	Title string
	Body  []string
	Hint  string
}

// RunNotice draws a notice and waits for the user to dismiss it.
//
// This exists because an overlay pane that exits immediately is invisible: the
// user presses a key, something flashes, and nothing explains itself. When the
// board cannot start — no VirtualBoard workspace here, no `vb`, an unsupported
// Herdr — it has to stay on screen long enough to say so.
func RunNotice(ctx context.Context, notice Notice, colour bool) error {
	screen, err := NewScreen(false)
	if err != nil {
		// No terminal at all: the caller is a script, so the ordinary error
		// path is the right one.
		return err
	}
	defer screen.Close()

	palette := NewPalette(colour)
	keys := ReadKeys(screen.Input())
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)

	draw := func() error {
		width, height := screen.Size()
		return screen.Draw(renderNotice(notice, palette, width, height))
	}
	if err := draw(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-resized:
			screen.Invalidate()
			if err := draw(); err != nil {
				return err
			}
		case key, ok := <-keys:
			if !ok {
				return nil
			}
			switch {
			case key.Name == KeyCtrlC, key.Name == KeyEsc, key.Rune == 'q':
				return nil
			}
		}
	}
}

func renderNotice(notice Notice, palette Palette, width, height int) []string {
	inner := width - 4
	if inner < 20 {
		inner = width
	}
	hint := notice.Hint
	if hint == "" {
		hint = "press q to close"
	}

	var body []string
	body = append(body, "", "  "+paint(palette.Bold, truncate(notice.Title, inner)), "")
	for _, line := range notice.Body {
		if line == "" {
			body = append(body, "")
			continue
		}
		for _, wrapped := range wrapText(line, inner-2) {
			body = append(body, "  "+wrapped)
		}
	}

	out := make([]string, 0, height)
	out = append(out, paint(palette.Warn, fit(" VirtualBoard", width)))
	for index := 0; index < height-2; index++ {
		if index < len(body) {
			out = append(out, fit(body[index], width))
			continue
		}
		out = append(out, strings.Repeat(" ", width))
	}
	out = append(out, paint(palette.Dim, fit(" "+hint, width)))
	return out
}

// NoWorkspaceNotice explains that there is no VirtualBoard workspace to show,
// and what to do about it.
func NoWorkspaceNotice(cwd string, cause error) Notice {
	return Notice{
		Title: "No VirtualBoard workspace here",
		Body: []string{
			fmt.Sprintf("hvb looked in %s and every directory above it for a .virtualboard/ directory, and did not find one.", cwd),
			"",
			"The board opens on whichever project your focused pane is in, so this usually means you were looking at a directory that is not a VirtualBoard project.",
			"",
			"To fix it, either:",
			"",
			"  • focus a pane inside a VirtualBoard project and open the board again, or",
			"  • run `vb init` in this project to create one.",
			"",
			fmt.Sprintf("(%v)", cause),
		},
	}
}
