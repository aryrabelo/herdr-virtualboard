package cli

import (
	"context"
	"testing"
)

// `hvb queue` has no VirtualBoard workspace, so it never calls Resolve — and
// Resolve is what used to build the Herdr client. Opening the board inside a
// Herdr pane then dereferenced a nil client and killed the process before it
// drew a single frame.
//
// The context is already cancelled: the callback must come back, not reach the
// network. What is under test is that it comes back at all.
func TestPaneTitleWorksWithoutAResolvedWorkspace(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p1")

	app := &App{}
	if app.Herdr() == nil {
		t.Fatal("the Herdr client is nil, so anything that talks to Herdr panics")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rename := app.paneTitle(ctx)
	if rename == nil {
		t.Fatal("inside a Herdr pane the board must get a rename callback")
	}
	rename("VirtualBoard queue")
}

// Outside a Herdr pane there is nothing to relabel, and the board must not be
// handed a callback that would try.
func TestPaneTitleIsNilOutsideAPane(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")

	if rename := (&App{}).paneTitle(context.Background()); rename != nil {
		t.Error("the board got a rename callback with no pane to rename")
	}
}
