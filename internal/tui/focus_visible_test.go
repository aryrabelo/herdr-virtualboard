package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// The owner watched the REVIEW column stop "brilhando" the day it hit zero
// cards: the only focus signal was `titleColour += Bold` over a status-coloured
// title, which reads as almost nothing, and an empty column had no card to lean
// on either. These tests pin a focus mark that survives both a zero-card column
// and NO_COLOR, on renderColumn directly so one column is isolated from the
// four beside it.

func focusModel(t *testing.T, pal Palette, specs ...*feature.Spec) *Model {
	t.Helper()
	model := NewModel(newFakeBackend(specs...), pal)
	model.Resize(160, 30)
	model.Reload(context.Background())
	return model
}

// lineContaining returns the first rendered row that carries needle, or fails.
func lineContaining(t *testing.T, rows []string, needle string) string {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row, needle) {
			return row
		}
	}
	t.Fatalf("no rendered row contains %q:\n%s", needle, strings.Join(rows, "\n"))
	return ""
}

// colourPalette forces the coloured palette regardless of the runner's
// environment: NewPalette disables colour when NO_COLOR is set to anything.
func colourPalette(t *testing.T) Palette {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	return NewPalette(true)
}

// TestFocusedEmptyColumnTitleShinesInColour is the owner's own case: he runs
// with colour, and a focused empty column must be unmistakable. The mark is
// Invert — a solid highlighted bar — not the old Bold, which the eye cannot see
// on an already-coloured title. Fails against the old `+= Bold`.
func TestFocusedEmptyColumnTitleShinesInColour(t *testing.T) {
	pal := colourPalette(t)
	model := focusModel(t, pal, spec("FTR-0001", "one", feature.Backlog))
	if got := len(model.Cards(feature.Review)); got != 0 {
		t.Fatalf("REVIEW is not empty (%d cards); the test needs the owner's zero-card case", got)
	}

	focused := lineContaining(t, model.renderColumn(feature.Review, 40, 20, true), "REVIEW")
	if !strings.Contains(focused, pal.Invert) {
		t.Fatalf("a focused empty column's title does not shine (no Invert): %q", focused)
	}

	plain := lineContaining(t, model.renderColumn(feature.Review, 40, 20, false), "REVIEW")
	if strings.Contains(plain, pal.Invert) {
		t.Fatalf("an unfocused column's title shines, so focus is not what draws it: %q", plain)
	}
}

// TestFocusedEmptyColumnIsVisibleWithoutColour proves the signal is not purely
// chromatic: with NewPalette(false) every escape is empty, so a colour-only
// mark would vanish. The caret is text, so the focused column still differs.
// Fails against the old code, where NO_COLOR made Bold empty and the two
// columns rendered byte for byte the same.
func TestFocusedEmptyColumnIsVisibleWithoutColour(t *testing.T) {
	model := focusModel(t, NewPalette(false), spec("FTR-0001", "one", feature.Backlog))
	if got := len(model.Cards(feature.Review)); got != 0 {
		t.Fatalf("REVIEW is not empty (%d cards)", got)
	}

	focused := strings.Join(model.renderColumn(feature.Review, 40, 20, true), "\n")
	plain := strings.Join(model.renderColumn(feature.Review, 40, 20, false), "\n")
	if focused == plain {
		t.Fatalf("without colour, a focused empty column is identical to an unfocused one:\n%s", plain)
	}

	title := lineContaining(t, model.renderColumn(feature.Review, 40, 20, true), "REVIEW")
	if !strings.Contains(title, focusCaretGlyph) {
		t.Fatalf("the focused title carries no text mark to survive NO_COLOR: %q", title)
	}
}

// TestFocusedEmptyColumnPlaceholderParticipates pins acceptance point 3: the
// empty-column placeholder itself must change under focus, not only the title.
// NO_COLOR again, so the difference has to be text.
func TestFocusedEmptyColumnPlaceholderParticipates(t *testing.T) {
	model := focusModel(t, NewPalette(false), spec("FTR-0001", "one", feature.Backlog))

	focused := lineContaining(t, model.renderColumn(feature.Review, 40, 20, true), "—")
	if !strings.Contains(focused, focusCaretGlyph) {
		t.Fatalf("the focused empty-column placeholder does not carry the focus caret: %q", focused)
	}

	plain := lineContaining(t, model.renderColumn(feature.Review, 40, 20, false), "—")
	if strings.Contains(plain, focusCaretGlyph) {
		t.Fatalf("an unfocused placeholder already carries the caret, so it marks nothing: %q", plain)
	}
}

// TestFocusedPopulatedColumnStillMarksSelectedCard guards what already worked:
// adding a title-bar highlight must not steal the selection highlight off the
// card. The selected card's row stays inverted; the sibling's does not.
func TestFocusedPopulatedColumnStillMarksSelectedCard(t *testing.T) {
	pal := colourPalette(t)
	model := focusModel(t, pal,
		spec("FTR-0001", "Alpha", feature.Review),
		spec("FTR-0002", "Beta", feature.Review),
	)
	cards := model.Cards(feature.Review)
	if len(cards) < 2 {
		t.Fatalf("need two REVIEW cards to tell selected from sibling, got %d", len(cards))
	}
	selected := cards[model.card[feature.Review]]
	sibling := cards[0]
	if sibling == selected {
		sibling = cards[1]
	}

	rows := model.renderColumn(feature.Review, 40, 30, true)
	selRow := lineContaining(t, rows, selected.Spec.Title)
	if !strings.Contains(selRow, pal.Invert) {
		t.Fatalf("the selected card lost its highlight in a focused column: %q", selRow)
	}
	sibRow := lineContaining(t, rows, sibling.Spec.Title)
	if strings.Contains(sibRow, pal.Invert) {
		t.Fatalf("an unselected card is highlighted, so the selection no longer reads: %q", sibRow)
	}
}

// TestHeaderAnnouncesReadingWhileReloading pins F1's contract: a backend read
// can take a minute, and the header has to say the board is reading so a slow
// board is not mistaken for a broken one. Legible on an empty board, so it is
// on the always-drawn header rather than in a column.
func TestHeaderAnnouncesReadingWhileReloading(t *testing.T) {
	model := focusModel(t, colourPalette(t), spec("FTR-0001", "one", feature.Backlog))

	if header := model.Render()[0]; strings.Contains(header, "reading") {
		t.Fatalf("an idle board already claims to be reading: %q", header)
	}
	if _, ok := model.BeginReload(); !ok {
		t.Fatal("the board refused to begin a reload it should have accepted")
	}
	if header := model.Render()[0]; !strings.Contains(header, "reading") {
		t.Fatalf("a reloading board does not say so in its header: %q", header)
	}
}
