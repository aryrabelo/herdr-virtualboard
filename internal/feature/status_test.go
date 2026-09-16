package feature

import "testing"

// The transition table is duplicated from vb-cli so the TUI can grey out
// impossible moves. If vb changes the lifecycle this test is the thing that
// should start failing, so it asserts the whole graph rather than a sample.
func TestAllowedTransitions(t *testing.T) {
	cases := []struct {
		from, to Status
		allowed  bool
	}{
		{Backlog, InProgress, true},
		{Backlog, Review, false},
		{Backlog, Done, false},
		{Backlog, Blocked, false},
		{InProgress, Review, true},
		{InProgress, Blocked, true},
		{InProgress, Done, false},
		{InProgress, Backlog, false},
		{Blocked, InProgress, true},
		{Blocked, Review, false},
		{Review, Done, true},
		{Review, InProgress, true},
		{Review, Blocked, false},
		{Done, InProgress, false},
		{Done, Review, false},
		{Done, Backlog, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to); got != tc.allowed {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.allowed)
		}
	}
}

func TestDoneIsTerminal(t *testing.T) {
	if !Done.Terminal() {
		t.Fatal("done must be terminal: VirtualBoard forbids moving a feature out of done")
	}
	for _, status := range []Status{Backlog, InProgress, Blocked, Review} {
		if status.Terminal() {
			t.Errorf("%s must not be terminal", status)
		}
	}
}

func TestParseStatus(t *testing.T) {
	cases := map[string]Status{
		"backlog":     Backlog,
		"IN-PROGRESS": InProgress,
		"in_progress": InProgress,
		"  review  ":  Review,
		"Done":        Done,
	}
	for input, want := range cases {
		got, ok := ParseStatus(input)
		if !ok || got != want {
			t.Errorf("ParseStatus(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	for _, input := range []string{"", "archived", "in progress", "wip"} {
		if got, ok := ParseStatus(input); ok {
			t.Errorf("ParseStatus(%q) = %q, true; want not ok", input, got)
		}
	}
}

func TestNextStatusesIsInBoardOrder(t *testing.T) {
	next := Review.NextStatuses()
	if len(next) != 2 || next[0] != InProgress || next[1] != Done {
		t.Fatalf("Review.NextStatuses() = %v; want [in-progress done] in board order", next)
	}
	if got := Done.NextStatuses(); len(got) != 0 {
		t.Fatalf("Done.NextStatuses() = %v; want empty", got)
	}
}

func TestDirMatchesVirtualBoardLayout(t *testing.T) {
	if got := InProgress.Dir(); got != "features/in-progress" {
		t.Fatalf("InProgress.Dir() = %q; want features/in-progress", got)
	}
}
