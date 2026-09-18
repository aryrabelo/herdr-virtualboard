package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
)

// queueRolesBackend is a board loaded with the owner's three charters instead
// of the upstream vocabulary fakeBackend ships. The order is the one Load
// returns — sorted by key — because it is what made the live defect visible:
// with the refusal dropped, the picker's index stayed at 0 and `cartografo`,
// first alphabetically, was the role sitting under `⏎ confirm`.
type queueRolesBackend struct{ *fakeBackend }

func (q queueRolesBackend) Roles() []roles.Role {
	return []roles.Role{
		{Key: "cartografo", Description: "Decompoe um esforco em folhas"},
		{Key: "executor", Description: "Implementa uma folha ja fatiada"},
		{Key: "grilling", Description: "Afia plano ou decisao por interrogatorio"},
	}
}

// Pressing d on a human-only card launches the unblocker straight away, with
// no picker in between: there is nothing to choose, because the dispatcher
// routes the card to one charter and refuses every other. What the owner
// asked for is that d on a blocked card start an agent that works out what he
// has to do, and a one-row picker would only charge a keystroke to say so.
//
// The implementer charters must be unreachable in every state, which is what
// "no picker" buys: this board ships three of them, and before any of this, d
// on ceo-bora#160 (labels `project:bugtoprompt hitl`) opened the picker with
// `cartografo` preselected and `⏎ confirm` ready to launch one.
func TestDispatchOnAHumanOnlyCardLaunchesTheUnblockerWithoutAPicker(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0160", "mintar credencial do gh", feature.InProgress, "hitl", "rumo:task"))
	model := newTestModel(t, queueRolesBackend{backend}, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'd'})

	if model.picker != nil || model.view == ViewDispatchPicker {
		t.Fatalf("a picker opened on a hitl card (view=%v, picker=%v)", model.view, model.picker != nil)
	}
	if len(backend.dispatch) != 1 {
		t.Fatalf("dispatch = %v, want exactly one launch", backend.dispatch)
	}
	// The board names no role: which charter a human-only card runs under
	// is the dispatcher's single decision, and a second copy here would be
	// one to keep in step. What it must never do is name an implementer.
	if got := backend.dispatch[0]; got != "FTR-0160/" {
		t.Errorf("the board dispatched %q, want it to leave the role to the dispatcher", got)
	}
	message, isError := model.statusMessage()
	if isError {
		t.Fatalf("status = %q, want the launch rather than a refusal", message)
	}
	if !strings.Contains(message, "FTR-0160") {
		t.Errorf("the status line does not name the card: %q", message)
	}
}

// The routing has one honest failure and this is it: no unblocker charter, so
// the board refuses and says what to write and where. It must never fall back
// to an implementer, which is the same defect as before wearing new clothes,
// and this runs the real dispatcher rather than the fake so the sentence under
// test is the one the dispatcher actually prints.
func TestDispatchOnAHumanOnlyCardRefusesWithNoUnblockerCharter(t *testing.T) {
	cfg := config.Default()
	// Both harnesses are unstartable, so a dispatch that wrongly got past
	// the role gate stops at the harness check and says so, instead of
	// reaching the nil Herdr this backend deliberately carries and dying
	// in a panic nobody can read.
	cfg.Harness, cfg.HumanOnlyHarness = unknownHarness, unknownHarness
	card := spec("FTR-0165", "comprar o dominio", feature.InProgress, "hitl", "rumo:task")
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'd'})

	if model.picker != nil || model.view == ViewDispatchPicker {
		t.Fatalf("a picker opened on a hitl card (view=%v, picker=%v)", model.view, model.picker != nil)
	}
	message, isError := model.statusMessage()
	if !isError {
		t.Fatalf("status = %q, want an error", message)
	}
	for _, want := range []string{"FTR-0165", "hitl", cfg.HumanOnlyRole, "agents"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not name %q: %q", want, message)
		}
	}
	// dispatchCharters ships backend_dev and qa. Neither may be offered as
	// a way out of a card only the owner can close.
	for _, implementer := range []string{"backend_dev", "qa"} {
		if strings.Contains(message, implementer) {
			t.Errorf("the refusal offers %q: %q", implementer, message)
		}
	}
}

// The harness is the other half of the routing, and this reads it where the
// board cannot fake it: the dispatcher is real, so an unstartable unblocker
// harness comes back named. A board that had ignored HumanOnlyHarness would
// answer with the ordinary one instead.
func TestDispatchOnAHumanOnlyCardUsesTheUnblockerHarness(t *testing.T) {
	cfg := config.Default()
	// Two harnesses, both unstartable and told apart by name. Whichever
	// one the dispatch resolved is the one the harness check quotes back,
	// and neither reaches this backend's nil Herdr.
	const ordinaryHarness = "not-the-unblocker-harness"
	cfg.Harness = ordinaryHarness
	cfg.HumanOnlyHarness = unknownHarness
	card := spec("FTR-0166", "aprovar o billing", feature.InProgress, "hitl")
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})
	// The charter the card routes to, so the dispatch gets past the role
	// gate and reaches the harness check that names the kind.
	backend.dispatcher.Roles = append(backend.dispatcher.Roles,
		roles.Role{Key: cfg.HumanOnlyRole, Name: cfg.HumanOnlyRole})
	model := newTestModel(t, backend, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'd'})

	if model.picker != nil {
		t.Fatalf("a picker opened on a hitl card: %v", model.view)
	}
	message, isError := model.statusMessage()
	if !isError || !strings.Contains(message, unknownHarness) {
		t.Fatalf("status = %q (error=%v), want the unblocker harness named", message, isError)
	}
	if strings.Contains(message, ordinaryHarness) {
		t.Errorf("the dispatch used the ordinary harness: %q", message)
	}
}

// The good path has to survive the gate: a `rumo:task` card with no `hitl`
// still opens the picker on its executor charter. Verified live, and it is the
// behaviour the refusal is one label away from breaking.
func TestDispatchPickerStillOpensOnTheSuggestedQueueRole(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0161", "fatiar o rumo", feature.InProgress, "rumo:task"))
	model := newTestModel(t, queueRolesBackend{backend}, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'd'})

	if model.view != ViewDispatchPicker || model.picker == nil {
		t.Fatal("d should open the dispatch picker on a card an agent can take")
	}
	chosen, ok := model.picker.current()
	if !ok || chosen.Value != "executor" {
		t.Fatalf("preselected %q, want executor", chosen.Value)
	}
}

// The board's other dispatch path: moving a card into in-progress offers to
// start an agent, and that offer calls DispatchWith with an empty role
// (update.go, offerDispatch). It reaches the same dispatcher, so it inherits
// the routing rather than needing one of its own — this drives the offer's
// own picker to prove it, instead of trusting the call graph. This board
// ships no unblocker charter, so the routing's honest failure is what comes
// back, and what must not come back is a dispatch under backend_dev.
func TestTheInProgressOfferInheritsTheHumanOnlyRouting(t *testing.T) {
	cfg := config.Default()
	// Neither harness can start, so a dispatch that wrongly resolved a
	// role reports that instead of reaching this backend's nil Herdr.
	cfg.Harness, cfg.HumanOnlyHarness = unknownHarness, unknownHarness
	card := spec("FTR-0162", "aprovar o billing na dashboard", feature.InProgress, "hitl", "rumo:task")
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})
	model := newTestModel(t, backend, 140, 30)
	ctx := context.Background()

	focused := model.FocusedCard()
	if focused == nil || focused.Spec.ID != card.ID {
		t.Fatalf("focused card = %v, want %s", focused, card.ID)
	}
	model.offerDispatch(focused, feature.InProgress)
	if model.picker == nil {
		t.Fatal("moving a card into in-progress stopped offering an agent")
	}
	// Row 1 is "Agent, in the project", the cheapest way to say yes.
	model.picker.index = 1
	model.Handle(ctx, Key{Name: KeyEnter})

	message, isError := model.statusMessage()
	if !isError || !strings.Contains(message, "FTR-0162") || !strings.Contains(message, cfg.HumanOnlyRole) {
		t.Fatalf("status = %q (error=%v), want the dispatcher's refusal", message, isError)
	}
	if strings.Contains(message, "harness") {
		t.Fatalf("the dispatch got past the role gate and failed on the harness instead: %q", message)
	}
}

// The same refusal on the board's direct dispatch call, through the real
// dispatcher rather than the fake backend: pressing ⏎ in the role picker, if it
// ever opened again, still cannot launch.
func TestTheBoardsDispatchCallRefusesHumanOnlyCards(t *testing.T) {
	cfg := config.Default()
	card := spec("FTR-0163", "plugar o hardware", feature.InProgress, "hitl")
	backend := dispatchingBackend(t, &cfg, &fakeSource{specs: []*feature.Spec{card}})
	ctx := context.Background()

	if _, err := backend.Dispatch(ctx, card, "qa", unknownHarness, false); !errors.Is(err, roles.ErrHumanOnly) {
		t.Fatalf("Dispatch err = %v, want roles.ErrHumanOnly even with an explicit role", err)
	}
	if _, err := backend.DispatchWith(ctx, card, "", unknownHarness, nil, nil); !errors.Is(err, roles.ErrHumanOnly) {
		t.Fatalf("DispatchWith err = %v, want roles.ErrHumanOnly", err)
	}
}
