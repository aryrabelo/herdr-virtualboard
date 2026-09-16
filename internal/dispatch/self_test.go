package dispatch

import (
	"context"
	"testing"

	"github.com/netors/herdr-virtualboard/internal/feature"
	"github.com/netors/herdr-virtualboard/internal/runs"
)

// An agent shelling out to `hvb run show` reconciles the board on the way past.
// It must never park its own run: it is the evidence that the run is alive, and
// Herdr can legitimately report the pane as `done` between turns.
func TestReconcileNeverParksTheCallersOwnRun(t *testing.T) {
	script := `case "$*" in
  *"pane list"*) echo '{"id":"x","result":{"panes":[]}}' ;;
  *"agent list"*) echo '{"id":"x","result":{"agents":[{"pane_id":"w1:p1","agent":"claude","agent_status":"done"}]}}' ;;
  *) echo '{"id":"x","result":{}}' ;;
esac`
	h := newHarness(t, map[string]feature.Status{"FTR-0001": feature.InProgress}, script)
	h.addRun("self", "FTR-0001", feature.InProgress, runs.Running, "w1:p1")
	h.addRun("other", "FTR-0001", feature.InProgress, runs.Running, "w1:p2")
	h.dispatcher.SelfRunID = "self"

	if _, err := h.dispatcher.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	self, err := h.dispatcher.Store.Get("self")
	if err != nil {
		t.Fatal(err)
	}
	if self.State != runs.Running {
		t.Fatalf("the caller's own run was parked as %q", self.State)
	}
	// Every other run is still reconciled normally.
	other, err := h.dispatcher.Store.Get("other")
	if err != nil {
		t.Fatal(err)
	}
	if other.State != runs.Awaiting {
		t.Fatalf("another run with a vanished pane = %q, want awaiting", other.State)
	}
}
