package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

// Options configure the event loop.
type Options struct {
	// Refresh is how often the board reloads on its own. The repository can
	// change under it — another agent's `vb move`, a git pull, a teammate —
	// and a board that only updates on keypress is quietly wrong.
	Refresh time.Duration
	// Colour enables the palette.
	Colour bool
	// PaneTitle, when set, is reported to Herdr so the plugin pane's border
	// names the project rather than the binary.
	PaneTitle func(title string)
}

// DefaultRefresh is the background reload interval. It is slow enough not to
// spam `vb` and `herdr` on an idle board, fast enough that a finished agent
// shows up before the user wonders.
const DefaultRefresh = 5 * time.Second

// Run draws the board until the user quits.
func Run(ctx context.Context, backend Backend, opts Options) error {
	if opts.Refresh <= 0 {
		opts.Refresh = DefaultRefresh
	}

	screen, err := NewScreen()
	if err != nil {
		return err
	}
	// A panic in a render path must not leave the user's terminal in raw
	// mode on the alternate buffer with no cursor.
	defer screen.Close()

	model := NewModel(backend, NewPalette(opts.Colour && term.IsTerminal(int(os.Stdout.Fd()))))
	width, height := screen.Size()
	model.Resize(width, height)
	model.Reload(ctx)

	if opts.PaneTitle != nil {
		opts.PaneTitle(fmt.Sprintf("VirtualBoard · %s", backend.ProjectName()))
	}

	keys := ReadKeys(screen.Input())
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)

	ticker := time.NewTicker(opts.Refresh)
	defer ticker.Stop()

	if err := screen.Draw(model.Render()); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil

		case key, ok := <-keys:
			if !ok {
				return nil
			}
			if !model.Handle(ctx, key) {
				continue
			}
			if model.Quitting() {
				return nil
			}
			if err := screen.Draw(model.Render()); err != nil {
				return err
			}

		case <-resized:
			width, height := screen.Size()
			model.Resize(width, height)
			screen.Invalidate()
			if err := screen.Draw(model.Render()); err != nil {
				return err
			}

		case <-ticker.C:
			// A background reload must never disturb an overlay: reloading
			// under an open form would discard what the user is typing.
			if model.view != ViewBoard && model.view != ViewDetail {
				continue
			}
			model.Reload(ctx)
			if err := screen.Draw(model.Render()); err != nil {
				return err
			}
		}
	}
}
