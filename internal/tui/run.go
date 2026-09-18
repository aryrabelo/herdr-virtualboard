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
	// EmptyHint is shown when the board holds no cards. The board cannot
	// word this itself: only the command knows which world it opened and
	// what the user would have to run to see the other one.
	EmptyHint string
	// FocusColumn is the column the board opens on, when the line has it.
	// A board opened to answer one question — what is waiting for review —
	// should not make the reader walk there first, and only the command
	// knows which question it was opened for.
	FocusColumn string
}

// DefaultRefresh is the background reload interval. It is slow enough not to
// spam `vb` and `herdr` on an idle board, fast enough that a finished agent
// shows up before the user wonders.
//
// It stays at five seconds even on a project whose read takes a minute,
// because the interval is no longer what governs the load: a tick that finds a
// read in flight is dropped, so the real refresh period is whatever the read
// itself costs. Five seconds is the promptness on a fast board and the
// round-up cost on a slow one, which is exactly one extra dropped tick.
const DefaultRefresh = 5 * time.Second

// display is the part of Screen the event loop drives: paint a frame, ask for
// the geometry, force a full repaint. It is an interface so the loop can be
// exercised without a terminal, which is the only way to test that the loop
// stays responsive while a reload is out.
type display interface {
	Draw(lines []string) error
	Size() (width, height int)
	Invalidate()
}

// Run draws the board until the user quits.
func Run(ctx context.Context, backend Backend, opts Options) error {
	if opts.Refresh <= 0 {
		opts.Refresh = DefaultRefresh
	}

	screen, err := NewScreen(true)
	if err != nil {
		return err
	}
	// A panic in a render path must not leave the user's terminal in raw
	// mode on the alternate buffer with no cursor.
	defer screen.Close()

	model := NewModel(backend, NewPalette(opts.Colour && term.IsTerminal(int(os.Stdout.Fd()))))
	model.emptyHint = opts.EmptyHint
	// The raw request: the model resolves it through the workflow's own
	// parser, which is what makes `ready_to_review` open the column
	// `ready-to-review` rather than falling back to wherever the work is.
	model.focusColumn = opts.FocusColumn
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

	return loop(ctx, model, screen, keys, resized, ticker.C)
}

// loop is the event loop over a prepared screen and its input sources.
//
// Reading the board is the one thing in here that is slow: it shells out to
// `vb`, to `gh`, and to whatever kit the project configured, and on a real
// project that has been measured at over a minute. So it does not happen on
// this goroutine. A read is started here, performed elsewhere, and its result
// comes back as just another event on the select — which is why a board that
// is reading still answers the keyboard, still redraws on resize, and still
// quits when told to.
func loop(
	ctx context.Context,
	model *Model,
	screen display,
	keys <-chan Key,
	resized <-chan os.Signal,
	tick <-chan time.Time,
) error {
	// A read in flight when the loop returns must not hold the exit. The
	// context it reads under dies here, so the external commands it is
	// waiting on are killed rather than waited for.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Capacity one is the whole reason the reload goroutine can never leak:
	// only one read is ever in flight, so its send always completes, even
	// against a loop that has already returned. Nothing ever closes this
	// channel, so a late result lands in the buffer instead of panicking.
	results := make(chan loadResult, 1)

	// The first read is started the same way as every other one, so the
	// board paints — and can be quit — before it has any data. Waiting for
	// the first read here would put the freeze back, just once.
	startReload(ctx, model, results)
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

		case result := <-results:
			// ApplyReload decides whether the answer is still worth having;
			// the frame is repainted either way, because the board has at
			// least stopped saying that it is reading.
			model.ApplyReload(result)
			if err := screen.Draw(model.Render()); err != nil {
				return err
			}

		case <-tick:
			if !startReload(ctx, model, results) {
				continue
			}
			// Nothing has arrived yet. The frame is repainted anyway: the
			// user is owed the fact that the board is reading, which is the
			// difference between a slow board and a broken one.
			if err := screen.Draw(model.Render()); err != nil {
				return err
			}
		}
	}
}

// startReload performs one backend read off the event loop's goroutine and
// reports whether it started one. It refuses when the board is not in a state
// to take a background result — see Model.BeginReload for both reasons.
//
// The goroutine touches nothing but the result it produces: the Model is
// mutated only by the loop, on the loop's goroutine, which is what makes the
// whole arrangement race-free without a single lock.
func startReload(ctx context.Context, model *Model, results chan<- loadResult) bool {
	token, ok := model.BeginReload()
	if !ok {
		return false
	}
	go func() { results <- model.Load(ctx, token) }()
	return true
}
