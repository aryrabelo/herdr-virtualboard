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

// Pressing d on a human-only card must not open the role picker at all.
//
// Measured live on the owner's board before this gate: d on ceo-bora#160
// (labels `project:bugtoprompt hitl`) opened the picker with `cartografo`
// preselected and `⏎ confirm` ready to launch an agent, because roles.Suggest's
// refusal was dropped into `_` and the unresolved suggestion left the index at
// 0. The card carries `rumo:task` too, which is the case that used to
// preselect a real executor charter.
func TestDispatchPickerRefusesAHumanOnlyCard(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0160", "mintar credencial do gh", feature.InProgress, "hitl", "rumo:task"))
	model := newTestModel(t, queueRolesBackend{backend}, 140, 30)
	model.Handle(context.Background(), Key{Rune: 'd'})

	if model.picker != nil || model.view == ViewDispatchPicker {
		t.Fatalf("the picker opened on a hitl card (view=%v, picker=%v)", model.view, model.picker != nil)
	}
	message, isError := model.statusMessage()
	if !isError {
		t.Fatalf("status = %q, want an error", message)
	}
	for _, want := range []string{"FTR-0160", "hitl"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not name %q: %q", want, message)
		}
	}
	// The reason is the owner's own hands, not a missing charter. This
	// board's charter directory is populated and correct, and sending the
	// user there is the mistake the no-charters message already fixed once.
	if strings.Contains(message, ".virtualboard/agents") {
		t.Errorf("the refusal blames the charter directory: %q", message)
	}
	if len(backend.dispatch) != 0 {
		t.Errorf("dispatch = %v, want nothing launched", backend.dispatch)
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
// the gate rather than needing one of its own — this drives the offer's own
// picker to prove it, instead of trusting the call graph.
//
// unknownHarness is what makes the assertion sharp: if the refusal were gone,
// the dispatcher would answer ErrUnknownHarness from the check that sits right
// after the role is resolved.
func TestTheInProgressOfferInheritsTheHumanOnlyGate(t *testing.T) {
	cfg := config.Default()
	cfg.Harness = unknownHarness
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
	if !isError || !strings.Contains(message, "FTR-0162") || !strings.Contains(message, "hitl") {
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
