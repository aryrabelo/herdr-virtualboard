package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// unknownHarness is a kind Herdr cannot start, and it is the stop these tests
// use inside Start. Start resolves the role, then validates the harness, then
// gates on Herdr — so a dispatch that got past the role gate reports
// ErrUnknownHarness, and one that did not reports the refusal. Both answers
// arrive without a pane, a socket, a vb lock or a git repository.
const unknownHarness = "no-such-harness"

// gatedDispatcher wires a dispatcher with the given charters and nothing else.
// Herdr and VB stay nil on purpose: a test that starts reaching either panics
// instead of quietly shelling out to the real binary, which is how these tests
// stay proof that the refusal happens before any launch at all.
func gatedDispatcher(t *testing.T, cfg *config.Config, charters []roles.Role) (*Dispatcher, *runs.Store) {
	t.Helper()
	t.Setenv("HVB_DATA_DIR", t.TempDir())
	store, err := runs.Open("dispatch-gate-test")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return &Dispatcher{
		Workspace: &workspace.Workspace{Root: root, Board: root},
		Config:    cfg,
		Store:     store,
		Roles:     charters,
	}, store
}

func hitlCard(id string, status feature.Status, labels ...string) *feature.Spec {
	return &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: id, Title: "mintar a credencial", Status: status,
		Labels: append([]string{"hitl"}, labels...)}}
}

// A workspace that ships no unblocker charter refuses a `hitl` card, and the
// refusal is where the routing is at its most dangerous: falling back to the
// default role here would re-create by a new door the defect the whole path
// exists to fix. So this asserts the two things together — refused, and
// nothing dispatched — plus that the message is fixable: it names the key it
// looked for and the directory to write it in.
//
// Measured live before any of this: the board's role picker opened on
// ceo-bora#160 (`project:bugtoprompt hitl`) with `cartografo` preselected and
// `⏎ confirm` ready, and resolveRole answered that confirmation with the
// configured default role — so the keystroke would have launched an agent.
func TestStartRefusesAHumanOnlyCardWithNoUnblockerCharter(t *testing.T) {
	charters := []roles.Role{{Key: "executor"}, {Key: "qa"}}
	for name, req := range map[string]Request{
		"no role asked for": {
			Spec: hitlCard("FTR-0160", feature.InProgress, "rumo:task"), Kind: unknownHarness,
		},
		// The explicit role loses to the routing. roles.Suggest already
		// decided that `hitl` beats the card's own `role:` label, and
		// answering differently one layer down would make the same card
		// land on an implementer depending on who asked.
		"explicit role asked for": {
			Spec: hitlCard("FTR-0160", feature.InProgress, "rumo:task"), Role: "executor", Kind: unknownHarness,
		},
		// Stock configuration gives the review column role = "qa", so a
		// branch placed after the explicit-role one would be bypassed
		// by the default config alone, with no flag from anybody.
		"column role from stock config": {
			Spec: hitlCard("FTR-0160", feature.Review), Kind: unknownHarness,
		},
	} {
		cfg := config.Default()
		dispatcher, store := gatedDispatcher(t, &cfg, charters)
		run, err := dispatcher.Start(context.Background(), req)
		if !errors.Is(err, roles.ErrHumanOnly) {
			t.Errorf("%s: Start err = %v, want roles.ErrHumanOnly", name, err)
			continue
		}
		for _, want := range []string{cfg.HumanOnlyRole, dispatcher.Workspace.AgentsDir()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q: %v", name, want, err)
			}
		}
		if strings.Contains(err.Error(), "executor") || strings.Contains(err.Error(), "qa") {
			t.Errorf("%s: the refusal offers an implementer charter: %v", name, err)
		}
		if run != nil {
			t.Errorf("%s: Start returned run %+v, want none", name, run)
		}
		stored, listErr := store.List()
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(stored) != 0 {
			t.Errorf("%s: the run store holds %d runs, want none — a refused dispatch must leave no record", name, len(stored))
		}
	}
}

// Turning the routing off is the operator's to do, and it degrades to the
// refusal this path started as — never to an implementer.
func TestStartRefusesAHumanOnlyCardWhenTheRoutingIsOff(t *testing.T) {
	cfg := config.Default()
	cfg.HumanOnlyRole = ""
	dispatcher, _ := gatedDispatcher(t, &cfg, []roles.Role{{Key: "executor"}, {Key: "destravador"}})

	_, err := dispatcher.Start(context.Background(), Request{
		Spec: hitlCard("FTR-0164", feature.InProgress), Role: "executor", Kind: unknownHarness})
	if !errors.Is(err, roles.ErrHumanOnly) {
		t.Fatalf("Start err = %v, want roles.ErrHumanOnly", err)
	}
	if strings.Contains(err.Error(), "destravador") {
		t.Errorf("the refusal points at a charter the operator switched off: %v", err)
	}
}

// The other half of the old bool, preserved: a workspace with no charters
// resolves no role, which is not a refusal. It still dispatches under the
// configured default role and the configured harness, which is what
// roles.Load's doc promises and what any board without an agents directory
// has always done. The unblocker routing must not reach this card — it is not
// human-only, it merely matched no charter.
func TestStartStillDispatchesWhenNoCharterMatches(t *testing.T) {
	cfg := config.Default()
	cfg.Role = "fullstack_dev"
	// The ordinary harness, made unstartable so Start stops one line past
	// the role gate and names the kind it resolved. That name is the
	// assertion: a card with no `hitl` must never pick up HumanOnlyHarness.
	cfg.Harness = unknownHarness
	dispatcher, _ := gatedDispatcher(t, &cfg, nil)
	card := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0161", Title: "fatiar o rumo", Status: feature.InProgress,
		Labels: []string{"rumo:task"}}}

	_, err := dispatcher.Start(context.Background(), Request{Spec: card})
	if errors.Is(err, roles.ErrHumanOnly) {
		t.Fatalf("Start refused a card no human is holding: %v", err)
	}
	if !errors.Is(err, ErrUnknownHarness) {
		t.Fatalf("Start err = %v, want the harness check that sits just past the role gate", err)
	}
	if !strings.Contains(err.Error(), unknownHarness) || strings.Contains(err.Error(), cfg.HumanOnlyHarness) {
		t.Fatalf("Start resolved the wrong harness for an ordinary card: %v", err)
	}
	// Start's own choice of role is only visible in the run record, which
	// it writes after the harness check this test stops on, so the role
	// itself is read from the function Start calls one line earlier.
	role, humanOnly, roleErr := dispatcher.resolveRole(Request{Spec: card}, cfg.Column(card.Status))
	if roleErr != nil || role.Key != "fullstack_dev" {
		t.Fatalf("resolveRole = %q (err=%v), want the configured default", role.Key, roleErr)
	}
	if humanOnly {
		t.Error("resolveRole called an unlabelled card human-only")
	}
}
