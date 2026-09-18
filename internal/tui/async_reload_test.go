package tui

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// slowBackend is a backend whose Load can be held open, the way the real one
// is held open by `kit.py fronteira` (measured at 69s on the owner's machine).
// Everything else is the fake backend, so the board behaves normally.
type slowBackend struct {
	fakeBackend

	mu      sync.Mutex
	loads   int
	blockAt int

	started chan int
	release chan struct{}
}

func newSlowBackend(blockAt int, specs ...*feature.Spec) *slowBackend {
	backend := &slowBackend{
		blockAt: blockAt,
		started: make(chan int, 16),
		release: make(chan struct{}),
	}
	backend.fakeBackend = *newFakeBackend(specs...)
	return backend
}

func (b *slowBackend) Load(ctx context.Context) ([]*feature.Spec, []*runs.Run, []error) {
	b.mu.Lock()
	b.loads++
	call := b.loads
	block := call >= b.blockAt
	b.mu.Unlock()

	b.started <- call
	if block {
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}

	b.mu.Lock()
	specs := b.specs
	b.mu.Unlock()
	return specs, nil, nil
}

func (b *slowBackend) loadCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loads
}

// probeDisplay stands in for the terminal. Each painted frame records a
// reading of the board taken by probe, which runs on the loop's own goroutine:
// that is what lets a test observe the model without racing it.
type probeDisplay struct {
	probe  func() string
	frames chan string
}

func (d *probeDisplay) Draw([]string) error {
	if d.probe != nil {
		d.frames <- d.probe()
	}
	return nil
}

func (d *probeDisplay) Size() (int, int) { return 160, 40 }
func (d *probeDisplay) Invalidate()      {}

// loopHarness runs the event loop in the background over a fake display.
type loopHarness struct {
	model   *Model
	keys    chan Key
	tick    chan time.Time
	resized chan os.Signal
	frames  chan string
	done    chan error
	cancel  context.CancelFunc
}

func startLoop(t *testing.T, backend Backend, probe func(*Model) string) *loopHarness {
	t.Helper()

	model := NewModel(backend, Palette{})
	model.Resize(160, 40)

	harness := &loopHarness{
		model:   model,
		keys:    make(chan Key, 8),
		tick:    make(chan time.Time, 8),
		resized: make(chan os.Signal, 1),
		frames:  make(chan string, 64),
		done:    make(chan error, 1),
	}
	screen := &probeDisplay{frames: harness.frames, probe: func() string { return probe(model) }}

	ctx, cancel := context.WithCancel(context.Background())
	harness.cancel = cancel
	go func() {
		harness.done <- loop(ctx, model, screen, harness.keys, harness.resized, harness.tick)
	}()
	return harness
}

// frame waits for the next painted frame.
func (h *loopHarness) frame(t *testing.T, why string) string {
	t.Helper()
	select {
	case reading := <-h.frames:
		return reading
	case <-time.After(3 * time.Second):
		t.Fatalf("no frame was painted %s: the event loop is not responding", why)
		return ""
	}
}

func (h *loopHarness) quit(t *testing.T) {
	t.Helper()
	h.keys <- Key{Rune: 'q'}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("the loop exited with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("the loop did not exit after q")
	}
}

// columnProbe reads the focused column. It runs on the loop's goroutine, so a
// test observes the model through the frames it painted rather than by racing
// the loop for its fields.
func columnProbe(m *Model) string { return strconv.Itoa(m.column) }

func waitLoad(t *testing.T, backend *slowBackend, which int) {
	t.Helper()
	select {
	case call := <-backend.started:
		if call != which {
			t.Fatalf("expected load #%d to start, got #%d", which, call)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("load #%d never started", which)
	}
}

// TestLoopAnswersKeysWhileReloadIsInFlight is the owner's bug: the board froze
// for as long as the external commands took, every refresh interval.
func TestLoopAnswersKeysWhileReloadIsInFlight(t *testing.T) {
	backend := newSlowBackend(2,
		spec("FTR-1", "backlog card", feature.Backlog),
		spec("FTR-2", "review card", feature.Review),
	)
	harness := startLoop(t, backend, columnProbe)
	defer harness.cancel()

	waitLoad(t, backend, 1)
	if got := harness.frame(t, "for the first board"); got != "0" {
		t.Fatalf("the board opened on column %s", got)
	}

	// A refresh falls due and the external commands hang.
	harness.tick <- time.Now()
	waitLoad(t, backend, 2)

	// The user presses right. This must be answered now, not in 69 seconds.
	harness.keys <- Key{Name: KeyRight}
	deadline := time.After(3 * time.Second)
	for {
		var reading string
		select {
		case reading = <-harness.frames:
		case <-deadline:
			t.Fatalf("the board never answered the key press while a reload was in flight")
		}
		if reading != "0" {
			if reading != "1" {
				t.Fatalf("the key moved the focus to column %s", reading)
			}
			break
		}
	}
	if backend.loadCount() != 2 {
		t.Fatalf("the reload was not still in flight: %d loads", backend.loadCount())
	}

	close(backend.release)
	harness.quit(t)
}

// TestTickDuringReloadDoesNotStartASecondLoad pins the drop-not-queue rule: at
// 5s a tick and a 69s read would otherwise pile up without bound.
func TestTickDuringReloadDoesNotStartASecondLoad(t *testing.T) {
	backend := newSlowBackend(2,
		spec("FTR-1", "backlog card", feature.Backlog),
		spec("FTR-2", "review card", feature.Review),
	)
	harness := startLoop(t, backend, columnProbe)
	defer harness.cancel()

	waitLoad(t, backend, 1)
	harness.frame(t, "for the first board")

	harness.tick <- time.Now()
	waitLoad(t, backend, 2)

	// Two more refreshes fall due while the first read is still out.
	harness.tick <- time.Now()
	harness.tick <- time.Now()

	// A key press is the fence: once the loop has answered it, it has
	// necessarily drained both ticks.
	harness.keys <- Key{Name: KeyRight}
	for harness.frame(t, "after the key press") == "0" {
	}

	if got := backend.loadCount(); got != 2 {
		t.Fatalf("ticks during an in-flight reload started %d loads, want 2", got)
	}

	close(backend.release)
	harness.quit(t)
}

// TestReloadResultIsDroppedUnderAnOverlay is the second half of the rule the
// old tick guard only had one half of.
func TestReloadResultIsDroppedUnderAnOverlay(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend(spec("FTR-1", "one", feature.Backlog))
	model := newTestModel(t, backend, 160, 40)

	token, ok := model.BeginReload()
	if !ok {
		t.Fatal("the board refused to start a reload on an idle board")
	}
	if !model.Reloading() {
		t.Fatal("the board does not report that it is reloading")
	}

	// The read is taken while the board is idle...
	backend.specs = append(backend.specs, spec("FTR-2", "two", feature.Backlog))
	result := model.Load(ctx, token)

	// ...and lands after the user opened the new-feature form and typed.
	model.Handle(ctx, Key{Rune: 'n'})
	model.Handle(ctx, Key{Rune: 'h'})
	model.Handle(ctx, Key{Rune: 'i'})
	if model.view != ViewNewFeature {
		t.Fatalf("the form did not open: view is %v", model.view)
	}

	model.ApplyReload(result)

	if model.Reloading() {
		t.Fatal("the board still reports a reload in flight after the result landed")
	}
	if model.view != ViewNewFeature {
		t.Fatalf("the reload closed the overlay: view is %v", model.view)
	}
	if got := model.form.values()["title"]; got != "hi" {
		t.Fatalf("the reload discarded the typed title: %q", got)
	}
	if got := len(model.Cards(feature.Backlog)); got != 1 {
		t.Fatalf("the reload swapped the board out from under the overlay: %d cards", got)
	}
}

// TestStaleReloadResultIsDropped: a read that started before something the
// user asked for describes an older board, and applying it would visibly undo
// their move — every mutation in update.go reloads after itself.
func TestStaleReloadResultIsDropped(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend(spec("FTR-1", "one", feature.Backlog))
	model := newTestModel(t, backend, 160, 40)

	token, ok := model.BeginReload()
	if !ok {
		t.Fatal("the board refused to start a reload on an idle board")
	}
	stale := model.Load(ctx, token)

	// The user presses r, which reloads synchronously and sees the truth.
	backend.specs = append(backend.specs, spec("FTR-2", "two", feature.Backlog))
	model.Reload(ctx)
	if got := len(model.Cards(feature.Backlog)); got != 2 {
		t.Fatalf("the synchronous reload read %d cards, want 2", got)
	}

	model.ApplyReload(stale)

	if got := len(model.Cards(feature.Backlog)); got != 2 {
		t.Fatalf("the stale result undid the fresher board: %d cards, want 2", got)
	}
	if model.Reloading() {
		t.Fatal("the board still reports a reload in flight")
	}
}

// TestQuitWithReloadInFlight: the exit must not wait on the external commands,
// and the read that is still out must have somewhere to land.
func TestQuitWithReloadInFlight(t *testing.T) {
	backend := newSlowBackend(2, spec("FTR-1", "one", feature.Backlog))
	baseline := runtime.NumGoroutine()
	harness := startLoop(t, backend, columnProbe)
	defer harness.cancel()

	waitLoad(t, backend, 1)
	harness.frame(t, "for the first board")

	harness.tick <- time.Now()
	waitLoad(t, backend, 2)

	harness.quit(t)

	// Now let the read finish, with nobody left to receive it. It must not
	// panic on a closed channel and must not block forever: the result
	// channel is buffered for exactly the one read that can be in flight.
	close(backend.release)
	waitForGoroutines(t, baseline)
}

// TestCancelWithReloadInFlight is the same exit by the other door.
func TestCancelWithReloadInFlight(t *testing.T) {
	backend := newSlowBackend(2, spec("FTR-1", "one", feature.Backlog))
	baseline := runtime.NumGoroutine()
	harness := startLoop(t, backend, columnProbe)

	waitLoad(t, backend, 1)
	harness.frame(t, "for the first board")

	harness.tick <- time.Now()
	waitLoad(t, backend, 2)

	// No release: a dead context must be enough to end the read, which is
	// what keeps a quit from waiting on `gh` for a minute.
	harness.cancel()
	select {
	case err := <-harness.done:
		if err != nil {
			t.Fatalf("the loop exited with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not exit when its context died")
	}
	waitForGoroutines(t, baseline)
}

// waitForGoroutines waits for the loop and its reload to be gone. A send that
// blocks forever shows up here as a goroutine that never returns.
func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		live := runtime.NumGoroutine()
		if live <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines are still live, was %d before the loop started", live, baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
