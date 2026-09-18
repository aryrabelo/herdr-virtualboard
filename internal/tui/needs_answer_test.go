package tui

import (
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// These tests read the rendered frame, never a helper. The refusal of a `hitl`
// card was born inert with a green test because the test exercised the helper
// instead of the path that calls it, so every assertion here goes through
// Model.Render.

// cardRows returns the three rows one card occupies — head, title, chips — as
// they appear in the frame, found by the id the card shows (cardID drops the
// FTR- prefix).
func cardRows(t *testing.T, lines []string, id string) []string {
	t.Helper()
	for index, line := range lines {
		if !strings.Contains(line, id) {
			continue
		}
		end := index + 3
		if end > len(lines) {
			end = len(lines)
		}
		return lines[index:end]
	}
	t.Fatalf("card %s is not on screen:\n%s", id, strings.Join(lines, "\n"))
	return nil
}

// blockedBoard is the fixture the owner's board has never produced: BLOCKED
// holding both of its populations at once.
//
// The `hitl` card waits on his own hands. The other one is what a
// frontier-blocked card looks like here — fila.ColumnFor consumes kit.py's
// blocked set and internal/issuesrc mints no label for it, so "blocked by
// another task" reaches the render as a Blocked card with no `hitl` label and
// nothing else to read it by.
func blockedBoard() *fakeBackend {
	return newFakeBackend(
		spec("FTR-0160", "Mintar a credencial da factory", feature.Blocked,
			"project:bugtoprompt", "hitl"),
		spec("FTR-0161", "Subir o deploy depois da 0160", feature.Blocked,
			"project:bugtoprompt"),
	)
}

// TestACardWaitingOnTheOwnerIsMarkedOnItsFace: he asked for "tem que ser claro
// que tá precisando de uma resposta minha", so the mark has to be on the card
// in the column, before anybody opens anything — and it has to be absent from
// the card next to it that is blocked on work instead of on him.
func TestACardWaitingOnTheOwnerIsMarkedOnItsFace(t *testing.T) {
	model := newTestModel(t, blockedBoard(), 140, 30)
	lines := model.Render()

	waiting := cardRows(t, lines, "0160")
	if !strings.Contains(strings.Join(waiting, "\n"), needsAnswerBadge) {
		t.Errorf("the hitl card does not carry %q on its face:\n%s",
			needsAnswerBadge, strings.Join(waiting, "\n"))
	}
	if !strings.Contains(waiting[0], needsAnswerGlyph) {
		t.Errorf("the hitl card's first row has no %q at its left edge: %q",
			needsAnswerGlyph, waiting[0])
	}

	blocked := strings.Join(cardRows(t, lines, "0161"), "\n")
	for _, unwanted := range []string{needsAnswerGlyph, "NEEDS YOU"} {
		if strings.Contains(blocked, unwanted) {
			t.Errorf("a card blocked on another task renders %q, which is the "+
				"mark for \"waiting on you\":\n%s", unwanted, blocked)
		}
	}
}

// TestTheHeaderCountsWhatWaitsOnTheOwner pins both shapes of the header's
// share, because the queue only has one of them today: all 6 BLOCKED cards are
// `hitl` (measured 2026-09-18), and the mixed column is what the header must
// still say correctly the day kit.py blocks a card that is not his.
func TestTheHeaderCountsWhatWaitsOnTheOwner(t *testing.T) {
	mixed := newTestModel(t, blockedBoard(), 140, 30).Render()[0]
	if want := "BLOCK 2 " + needsAnswerGlyph + "1"; !strings.Contains(mixed, want) {
		t.Errorf("header does not say %q — one of the two blocked cards waits "+
			"on him: %q", want, mixed)
	}

	both := blockedBoard()
	both.specs[1].Labels = append(both.specs[1].Labels, "hitl")
	whole := newTestModel(t, both, 140, 30).Render()[0]
	if want := "BLOCK 2" + needsAnswerGlyph; !strings.Contains(whole, want) {
		t.Errorf("header does not say %q — the whole column waits on him: %q", want, whole)
	}
	if dup := needsAnswerGlyph + "2"; strings.Contains(whole, dup) {
		t.Errorf("header prints %q next to BLOCK 2: a share that restates the "+
			"column's own digit is noise: %q", dup, whole)
	}
}

// TestAnOrdinaryCardIsNotMarked: the mark is worth nothing if it is on
// everything. `rumo:task` is a decision kind, not a block on a human.
func TestAnOrdinaryCardIsNotMarked(t *testing.T) {
	backend := newFakeBackend(
		spec("FTR-0150", "Pagar a divida de lint", feature.Backlog, "rumo:task", "project:bugtoprompt"),
	)
	frame := strings.Join(newTestModel(t, backend, 140, 30).Render(), "\n")
	for _, unwanted := range []string{needsAnswerGlyph, "NEEDS YOU"} {
		if strings.Contains(frame, unwanted) {
			t.Errorf("an ordinary card put %q on the board:\n%s", unwanted, frame)
		}
	}
}

// TestAFinishedCardIsNotWaitingOnAnybody: fila.ColumnFor settles `closed`
// before any label, so a `hitl` card in DONE is one he already unblocked.
// Counting it would make the header ask him for an answer he has given.
func TestAFinishedCardIsNotWaitingOnAnybody(t *testing.T) {
	backend := newFakeBackend(
		spec("FTR-0132", "Testar o Pro de ponta a ponta", feature.Done, "hitl"),
	)
	lines := newTestModel(t, backend, 140, 30).Render()
	if rows := strings.Join(cardRows(t, lines, "0132"), "\n"); strings.Contains(rows, "NEEDS YOU") {
		t.Errorf("a closed hitl card still asks for an answer:\n%s", rows)
	}
	if strings.Contains(lines[0], needsAnswerGlyph) {
		t.Errorf("the header counts a closed hitl card as waiting on him: %q", lines[0])
	}
}

// TestTheLabelIsReadTheWayTheRestOfTheTreeReadsIt: fila.ColumnFor and
// roles.Suggest both compare case-insensitively after trimming, so a card
// labelled `HITL` routes to the unblocker. A render that only matched the
// lowercase spelling would send it there with no mark on screen.
func TestTheLabelIsReadTheWayTheRestOfTheTreeReadsIt(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0170", "Aprovar o billing", feature.Blocked, " HITL "))
	lines := newTestModel(t, backend, 140, 30).Render()
	if rows := strings.Join(cardRows(t, lines, "0170"), "\n"); !strings.Contains(rows, needsAnswerBadge) {
		t.Errorf("a card labelled \" HITL \" is unmarked:\n%s", rows)
	}
}

// TestNeedsAnswerIsNotTheFailureColour is the colour budget, pinned. 203 is
// already the failed run, the cancelled run and the P0; spending it on the 6
// of 33 cards that are `hitl` would teach the eye that red means furniture.
func TestNeedsAnswerIsNotTheFailureColour(t *testing.T) {
	// The harness runs with NO_COLOR=1 set, which empties every field.
	t.Setenv("NO_COLOR", "")
	palette := NewPalette(true)
	if palette.NeedsAnswer == "" {
		t.Fatal("a colour palette must give the needs-answer mark a colour")
	}
	if palette.NeedsAnswer == palette.Failed || palette.NeedsAnswer == palette.Warn {
		t.Errorf("needs-answer is painted with the failure colour %q", palette.NeedsAnswer)
	}
}
