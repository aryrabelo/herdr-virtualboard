package tui

import (
	"context"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// A lone Esc reaching the board must not close it. The terminal is free to
// split a read between an escape sequence's ESC byte and the rest of it, and
// every such split arrives here as a bare Esc: an arrow key, or — since mouse
// reporting — a wheel notch. Closing the board on that is losing the owner's
// session to a read boundary, so `q` and ctrl-C are the only ways out.
func TestABareEscapeDoesNotCloseTheBoard(t *testing.T) {
	model := newTestModel(t, newFakeBackend(spec("FTR-0001", "A", feature.Backlog)), 140, 30)
	ctx := context.Background()

	model.Handle(ctx, Key{Name: KeyEsc})

	if model.Quitting() {
		t.Fatal("a bare Esc on the board closed it; only q and ctrl-C may quit")
	}
	if model.view != ViewBoard {
		t.Fatalf("Esc moved the board off its own view: %v", model.view)
	}
}

// The verb it replaced still works, or the fix above would have taken the exit
// away rather than moved it.
func TestQStillQuitsTheBoard(t *testing.T) {
	model := newTestModel(t, newFakeBackend(spec("FTR-0001", "A", feature.Backlog)), 140, 30)

	model.Handle(context.Background(), Key{Rune: 'q'})

	if !model.Quitting() {
		t.Fatal("q must still quit the board")
	}
}

// Esc keeps the job where it is irreplaceable: `q` in the new-feature form is
// a letter typed into the field, so nothing else cancels it.
func TestEscapeStillCancelsTheNewFeatureForm(t *testing.T) {
	model := newTestModel(t, newFakeBackend(), 140, 30)
	ctx := context.Background()

	model.Handle(ctx, Key{Rune: 'n'})
	if model.view != ViewNewFeature {
		t.Fatal("n should open the new-feature form")
	}

	model.Handle(ctx, Key{Name: KeyEsc})

	if model.view != ViewBoard {
		t.Fatalf("Esc must cancel the form; view = %v", model.view)
	}
	if model.Quitting() {
		t.Fatal("cancelling the form must not quit")
	}
}
