package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// fakeSource is a read-only source that returns whatever a test hands it. It
// satisfies tui.Source structurally, exactly as internal/fios and
// internal/ghboard do, so the composition is testable without a vault on disk
// and without the gh binary.
type fakeSource struct {
	specs    []*feature.Spec
	problems []error
	loads    int
}

func (f *fakeSource) Load(context.Context) ([]*feature.Spec, []error) {
	f.loads++
	return f.specs, f.problems
}

// fiosCard is a card as internal/fios reports it.
func fiosCard(id, title string, status feature.Status, path string) *feature.Spec {
	card := spec(id, title, status, LabelSourceFios)
	card.Path = path
	card.Owner = "ary"
	return card
}

// prCard is a card as internal/ghboard reports it, with the labels the board
// draws its extra facts from.
func prCard(number, title string, status feature.Status, labels ...string) *feature.Spec {
	card := spec("PR-"+number, title, status, append([]string{LabelSourcePR, labelPRPrefix + number}, labels...)...)
	card.Owner = "factory-bora[bot]"
	card.Priority = ""
	card.Updated = time.Now().Add(-41 * time.Minute).UTC().Format(time.RFC3339)
	return card
}

// A board that reads two sources must survive one of them failing. Going blank
// because GitHub is unreachable hides the owner's own queue for a reason that
// has nothing to do with it.
func TestLoadKeepsTheWorkingSourceWhenTheOtherFails(t *testing.T) {
	broken := &fakeSource{problems: []error{errors.New("gh: exit 4")}}
	working := &fakeSource{specs: []*feature.Spec{
		fiosCard("FIO-3", "Ligar o board na fila real", feature.Backlog, "/vault/FIOS.md"),
		fiosCard("FIO-4", "Fechar a rodada do jarvis", feature.Blocked, "/vault/FIOS.md"),
	}}
	backend := NewSourceBackend("vault + repo", nil, "ary", broken, working)

	specs, runList, problems := backend.Load(context.Background())

	if len(specs) != 2 {
		t.Fatalf("got %d cards from the working source, want 2", len(specs))
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "gh: exit 4") {
		t.Fatalf("the broken source's error did not survive: %v", problems)
	}
	if len(runList) != 0 {
		t.Fatalf("a board with no run store reported %d runs", len(runList))
	}
	if broken.loads != 1 || working.loads != 1 {
		t.Fatalf("each source must be asked exactly once: broken=%d working=%d", broken.loads, working.loads)
	}
}

// Every source here owns its file. A refusal that does not say where to go
// leaves the user pressing a key that silently does nothing.
func TestMutationsAreRefusedNamingTheOwningFile(t *testing.T) {
	gate := spec("GATE-factory-cnb-2", "Aprovar o protótipo", feature.Blocked,
		LabelSourceGates, labelGatePrefix+"factory-cnb.md")
	gate.Path = "/vault/gates/factory-cnb.md"

	backend := NewSourceBackend("vault", nil, "ary", &fakeSource{specs: []*feature.Spec{
		fiosCard("FIO-3", "Ligar o board na fila real", feature.Backlog, "/vault/FIOS.md"),
		gate,
		prCard("179", "board: ler a fila real", feature.Review, labelIssuePrefix+"168"),
	}})
	backend.Load(context.Background())

	ctx := context.Background()
	cases := []struct {
		name string
		call func() error
		want []string
	}{
		{"move a fio", func() error {
			return backend.Move(ctx, "FIO-3", feature.InProgress, "ary")
		}, []string{"FIO-3", "FIOS.md", "bin/fios-check"}},
		{"set a field on a gate", func() error {
			return backend.SetField(ctx, "GATE-factory-cnb-2", "priority", "P0")
		}, []string{"GATE-factory-cnb-2", "factory-cnb.md"}},
		{"move a pull request", func() error {
			return backend.Move(ctx, "PR-179", feature.Done, "ary")
		}, []string{"PR-179", "gh pr view 179"}},
		{"dispatch onto a pull request", func() error {
			_, err := backend.Dispatch(ctx, prCard("179", "board", feature.Review), "qa", "claude", false)
			return err
		}, []string{"PR-179", "vb"}},
		{"create a card", func() error {
			_, err := backend.Create(ctx, "algo novo", nil, "P1")
			return err
		}, []string{"algo novo", "FIOS.md"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.call()
			if err == nil {
				t.Fatal("the board accepted a write to a read-only source")
			}
			if !errors.Is(err, ErrReadOnly) {
				t.Errorf("refusal is not an ErrReadOnly: %v", err)
			}
			for _, want := range testCase.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// A cancelled pull request is done without having landed. It must not sit in
// Done pretending it shipped, and the user must be able to walk to it: a column
// the cursor cannot reach is worse than no column at all.
func TestCancelledCardsGetTheirOwnReachableColumn(t *testing.T) {
	landed := prCard("178", "harness: fechar o gate", feature.Done, labelIssuePrefix+"167")
	dropped := prCard("179", "board: abandonado", feature.Done, LabelCanceled, labelIssuePrefix+"168")
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{
		specs: []*feature.Spec{landed, dropped},
	})
	model := newTestModel(t, backend, 160, 30)

	counts := model.Counts()
	if counts[feature.Done] != 1 {
		t.Errorf("Done holds %d cards, want only the merged one", counts[feature.Done])
	}
	if counts[StatusCanceled] != 1 {
		t.Errorf("the terminal column holds %d cards, want the cancelled one", counts[StatusCanceled])
	}
	if cards := model.Cards(StatusCanceled); len(cards) != 1 || cards[0].Spec.ID != "PR-179" {
		t.Fatalf("the cancelled column holds %+v", cards)
	}

	// Walk the cursor with the keys a user presses, from the leftmost column
	// to the far right, and stop where it stops.
	ctx := context.Background()
	model.column = 0
	for step := 0; step < 20; step++ {
		model.Handle(ctx, Key{Name: KeyRight})
	}
	if got := model.FocusedStatus(); got != StatusCanceled {
		t.Fatalf("walking right ends on %q, so the cancelled column is unreachable", got)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "PR-179" {
		t.Fatalf("the cursor reached the column but not the card: %+v", card)
	}

	// And it is a real column on screen: counted in the header, and with its
	// card actually drawn in the body. The two are asserted apart because the
	// header segment and the column title render the same text, so a single
	// whole-screen match would let either one alone satisfy it.
	frame := model.Render()
	if !strings.Contains(frame[0], "CANCELED 1") {
		t.Errorf("the header does not count the cancelled column: %q", frame[0])
	}
	body := strings.Join(frame[1:], "\n")
	if !strings.Contains(body, "CANCELED 1") {
		t.Errorf("the board draws no cancelled column\n%s", body)
	}
	if !strings.Contains(body, "board: abandonado") {
		t.Errorf("the cancelled card itself is not drawn\n%s", body)
	}
}

// Opening a board whose only work sits in the derived column must land the
// cursor there. Starting on an empty Backlog makes the user navigate before the
// board tells them anything.
func TestBoardOpensWhereTheWorkIsEvenWhenItIsCancelled(t *testing.T) {
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{specs: []*feature.Spec{
		prCard("179", "board: abandonado", feature.Done, LabelCanceled),
	}})
	model := newTestModel(t, backend, 160, 30)

	if got := model.FocusedStatus(); got != StatusCanceled {
		t.Fatalf("the board opened on %q, not on the only populated column", got)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "PR-179" {
		t.Fatalf("the board opened on no card: %+v", card)
	}
}

// A pull request that closes unmerged while the user is looking at it changes
// column. The cursor must follow the card, which is the whole reason Reload
// re-finds the selection by identity instead of by index.
func TestReloadFollowsACardIntoTheCancelledColumn(t *testing.T) {
	open := prCard("179", "board: ler a fila real", feature.Review, labelIssuePrefix+"168")
	source := &fakeSource{specs: []*feature.Spec{open}}
	model := newTestModel(t, NewSourceBackend("repo", nil, "ary", source), 160, 30)

	if got := model.FocusedStatus(); got != feature.Review {
		t.Fatalf("the board opened on %q, want review", got)
	}

	// The pull request is closed without merging.
	source.specs = []*feature.Spec{
		prCard("179", "board: ler a fila real", feature.Done, LabelCanceled, labelIssuePrefix+"168"),
	}
	model.Reload(context.Background())

	if got := model.FocusedStatus(); got != StatusCanceled {
		t.Fatalf("the cursor stayed on %q after the card moved", got)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "PR-179" {
		t.Fatalf("the cursor lost the card it was following: %+v", card)
	}
}

// A reload that drops the focused card must leave the cursor on a card that is
// still there. A column drawing two cards with nothing selected is how a user
// presses a key and watches it go nowhere.
func TestReloadClampsTheCursorInsideTheCancelledColumn(t *testing.T) {
	first := prCard("178", "primeiro abandonado", feature.Done, LabelCanceled)
	second := prCard("179", "segundo abandonado", feature.Done, LabelCanceled)
	source := &fakeSource{specs: []*feature.Spec{first, second}}
	model := newTestModel(t, NewSourceBackend("repo", nil, "ary", source), 160, 30)

	ctx := context.Background()
	model.Handle(ctx, Key{Name: KeyDown})
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "PR-179" {
		t.Fatalf("could not select the second cancelled card: %+v", card)
	}

	source.specs = []*feature.Spec{first}
	model.Reload(ctx)

	if card := model.FocusedCard(); card == nil || card.Spec.ID != "PR-178" {
		t.Fatalf("after the selected card vanished the cursor points at %+v", card)
	}
}

// The header's run counter is the board's only whole-board signal. A run in the
// derived column is as live as any other and must be counted.
//
// The card needs the pull-request source as well as the cancelled label,
// because that is what puts it in the derived column at all — with the label
// alone it sits in Done and this test would pass while proving nothing.
func TestActiveRunsInTheCancelledColumnAreCounted(t *testing.T) {
	card := spec("PR-7", "abandonado com agente vivo", feature.Done, LabelSourcePR, LabelCanceled)
	backend := newFakeBackend(card)
	backend.runs = []*runs.Run{{
		ID: "r-1", FeatureID: "PR-7", Role: "qa", Kind: "claude",
		State: runs.Running, PaneID: "w1:p1", StartedAt: time.Now(),
	}}
	model := newTestModel(t, backend, 160, 30)

	// The guard against the guard: if this card were not in the derived
	// column, everything below would pass without testing it.
	if got := len(model.Cards(StatusCanceled)); got != 1 {
		t.Fatalf("the derived column holds %d cards, so this test proves nothing", got)
	}
	if got := model.ActiveRunCount(); got != 1 {
		t.Fatalf("the board counts %d active runs, want 1", got)
	}
	if frame := model.Render(); !strings.Contains(frame[0], "▶1") {
		t.Errorf("the header does not report the live run: %q", frame[0])
	}
}

// The narrow layout replaces the columns with a breadcrumb. The derived column
// has to appear in it, or a user in a split pane cannot tell where they are.
func TestCompactBreadcrumbIncludesTheCancelledColumn(t *testing.T) {
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{specs: []*feature.Spec{
		prCard("178", "entregue", feature.Done),
		prCard("179", "board: abandonado", feature.Done, LabelCanceled),
	}})
	model := newTestModel(t, backend, 100, 30)

	ctx := context.Background()
	model.column = 0
	for step := 0; step < 20; step++ {
		model.Handle(ctx, Key{Name: KeyRight})
	}
	if got := model.FocusedStatus(); got != StatusCanceled {
		t.Fatalf("walking right in the narrow layout ends on %q", got)
	}

	// Frame line 0 is the header; line 1 is the breadcrumb.
	crumbs := model.Render()[1]
	if !strings.Contains(crumbs, "CANCELED") {
		t.Errorf("the breadcrumb omits the column the cursor is on: %q", crumbs)
	}
	if !strings.Contains(crumbs, "DONE") {
		t.Errorf("the breadcrumb lost the lifecycle: %q", crumbs)
	}
}

// Without a cancelled card the board is exactly the five-column VirtualBoard
// lifecycle: the derived column must not appear on a spec-markdown board.
func TestTheDerivedColumnStaysHiddenWhenEmpty(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "First thing", feature.Done))
	model := newTestModel(t, backend, 160, 30)

	if strings.Contains(screen(model), "CANCELED") {
		t.Error("a board with nothing cancelled grew a sixth column")
	}
	model.column = 0
	for step := 0; step < 20; step++ {
		model.Handle(context.Background(), Key{Name: KeyRight})
	}
	if got := model.FocusedStatus(); got != feature.Done {
		t.Errorf("walking right ends on %q, want done", got)
	}
}

// The card facts the approved design asks for: the pull-request number, the
// linked issue, the outside author, and how old the item is.
//
// The geometry is the narrow one-column layout, which is how the board is read
// in a split pane and the only width where a card has room for every fact. On a
// five-column board a 31-column card truncates the meta row, exactly as it
// already does for a long role name.
func TestCardShowsThePullRequestFacts(t *testing.T) {
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{specs: []*feature.Spec{
		prCard("179", "board: ler a fila real", feature.Review,
			labelIssuePrefix+"168", LabelExternal, LabelDraft),
	}})
	model := newTestModel(t, backend, 100, 30)

	out := screen(model)
	for _, want := range []string{"#179", "factory-bora[bot]", "Issue #168", "Draft", "External", "41m"} {
		if !strings.Contains(out, want) {
			t.Errorf("the card does not show %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "PR-179") {
		t.Errorf("the card still shows the internal id form\n%s", out)
	}
}

// The spec-markdown board must look exactly as it did. A VirtualBoard spec
// carries created/updated dates too, so the new card facts have to be gated on
// the source, not merely on the fields happening to be filled: the first cut of
// this put an age on every card of the vb board.
func TestVirtualBoardCardsGainNothing(t *testing.T) {
	vbSpec := spec("FTR-0001", "Primeiro spec do vb", feature.Backlog)
	vbSpec.Complexity = "M"
	vbSpec.Owner = "ary"

	model := newTestModel(t, newFakeBackend(vbSpec), 160, 30)
	card := model.Cards(feature.Backlog)[0]
	if extra := model.sourceMeta(card); len(extra) != 0 {
		t.Errorf("a vb card grew %v", extra)
	}

	// The same card, once a source claims it, does show its age.
	vbSpec.Labels = []string{LabelSourceFios}
	vbSpec.Updated = time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339)
	if extra := model.sourceMeta(card); len(extra) != 1 || !strings.Contains(extra[0], "1h") {
		t.Errorf("a source card shows %v, want its age", extra)
	}
}

// An id is an opaque string: a source may mint it from a content hash so a
// stored run keeps pointing at the card it was dispatched for. The board shows
// a pull request by its GitHub number, which it reads off the pr: label, and
// leaves every other id alone apart from VirtualBoard's own FTR- prefix.
func TestCardIDTreatsTheIDAsOpaque(t *testing.T) {
	cases := []struct {
		spec *feature.Spec
		want string
	}{
		{spec("FTR-0001", "vb card", feature.Backlog), "0001"},
		{spec("a91f3c2", "hashed fio", feature.Backlog, LabelSourceFios), "a91f3c2"},
		{spec("GATE-factory-cnb-2", "gate line", feature.Blocked, LabelSourceGates), "GATE-factory-cnb-2"},
		// The number comes from the label, whatever the id happens to be.
		{spec("7f3a9b1", "pull request", feature.Review, LabelSourcePR, labelPRPrefix+"179"), "#179"},
	}
	for _, testCase := range cases {
		if got := cardID(testCase.spec); got != testCase.want {
			t.Errorf("cardID(%q) = %q, want %q", testCase.spec.ID, got, testCase.want)
		}
	}
}

// The terminal column means one thing: a pull request that closed without
// merging. It is not the drawer for whatever did not fit, so a done card with
// any other label stays in Done.
//
// The last card is the one that matters. A source passes a repository's real
// labels through verbatim, and a repository may have a label named literally
// `state:canceled` — so the label alone cannot be trusted to mean the board's
// own verdict. Requiring the pull-request source keeps every other source out
// of the column no matter what its items are labelled.
func TestTheTerminalColumnIsNotACatchAll(t *testing.T) {
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{specs: []*feature.Spec{
		spec("GATE-x-1", "resolvido de outra forma", feature.Done, LabelSourceGates, "resolved:other"),
		spec("FIO-9", "entregue", feature.Done, LabelSourceFios, "closed", "stale"),
		spec("FIO-10", "fio com label de outro mundo", feature.Done, LabelSourceFios, LabelCanceled),
		prCard("179", "fechado sem merge", feature.Done, LabelCanceled),
	}})
	model := newTestModel(t, backend, 160, 30)

	if got := model.Counts()[feature.Done]; got != 3 {
		t.Errorf("Done holds %d cards, want the three that are merely done", got)
	}
	cards := model.Cards(StatusCanceled)
	if len(cards) != 1 || cards[0].Spec.ID != "PR-179" {
		t.Fatalf("the terminal column holds %+v, want only the cancelled pull request", cards)
	}
}

// A pull request is the only card that carries labels it did not mint: the
// repository's own labels ride along verbatim. So it is the only card that can
// arrive claiming to be something else — a forged `hvb:source:fios` on a pull
// request must change neither its column nor where the board sends the user.
//
// The source drops repository labels inside the reserved namespace, so this
// should never arrive. That is precisely why the board must not depend on it:
// two independent reasons to be right is the point.
func TestAForgedSourceLabelChangesNothing(t *testing.T) {
	merged := prCard("178", "entregue", feature.Done, LabelSourceFios)
	dropped := prCard("179", "fechado sem merge", feature.Done, LabelCanceled, LabelSourceFios, LabelSourceGates)
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{
		specs: []*feature.Spec{merged, dropped},
	})
	model := newTestModel(t, backend, 160, 30)

	// The merged one stays shipped; the dead one stays dead.
	if got := model.Counts()[feature.Done]; got != 1 {
		t.Errorf("Done holds %d cards, want the merged pull request", got)
	}
	cards := model.Cards(StatusCanceled)
	if len(cards) != 1 || cards[0].Spec.ID != "PR-179" {
		t.Fatalf("the terminal column holds %+v, want only the dead pull request", cards)
	}

	// And the refusal still sends the user to GitHub, not to FIOS.md.
	err := backend.Move(context.Background(), "PR-179", feature.InProgress, "ary")
	if err == nil {
		t.Fatal("the board accepted a write to a pull request")
	}
	if !strings.Contains(err.Error(), "gh pr view 179") {
		t.Errorf("the refusal does not point at GitHub: %v", err)
	}
	if strings.Contains(err.Error(), "FIOS.md") {
		t.Errorf("a forged label sent the user to the wrong file: %v", err)
	}
}

// An empty board is the one state the columns cannot express: a clean queue
// and a source that failed draw the same dashes. Measured on the real board —
// pointing it at an unreachable repository dropped 39 cards and reported
// "1 spec(s) could not be read", which reads like a single bad file.
func TestAnEmptyBoardSaysWhyItIsEmpty(t *testing.T) {
	failed := NewSourceBackend("repo", nil, "ary",
		&fakeSource{problems: []error{errors.New("gh: could not resolve to a Repository")}})
	clean := NewSourceBackend("repo", nil, "ary", &fakeSource{})

	broken := strings.Join(newTestModel(t, failed, 160, 30).Render(), "\n")
	if !strings.Contains(broken, "a source failed") {
		t.Errorf("a board emptied by a dead source does not say so:\n%s", broken)
	}
	if strings.Contains(broken, "nothing is open") {
		t.Errorf("a dead source was reported as an empty queue:\n%s", broken)
	}

	drained := strings.Join(newTestModel(t, clean, 160, 30).Render(), "\n")
	if !strings.Contains(drained, "nothing is open") {
		t.Errorf("a clean queue does not say so:\n%s", drained)
	}
	if strings.Contains(drained, "a source failed") {
		t.Errorf("a clean queue was reported as a failure:\n%s", drained)
	}
}

// The caller words the hint because only it knows which of the two worlds it
// opened. Without this, `hvb tui` on a workspace with no specs is a blank
// board that never mentions `hvb queue`.
func TestTheEmptyBoardCarriesTheCallersHint(t *testing.T) {
	model := newTestModel(t, NewSourceBackend("repo", nil, "ary", &fakeSource{}), 160, 30)
	model.emptyHint = "run `hvb queue` for the owner's queue"

	out := strings.Join(model.Render(), "\n")
	if !strings.Contains(out, "hvb queue") {
		t.Errorf("the hint never reached the screen:\n%s", out)
	}
}

// A board that has not loaded yet has not found nothing. Claiming an empty
// queue before the first read is the same lie pointing the other way, and it
// is the state every board passes through.
func TestABoardThatHasNotLoadedClaimsNothing(t *testing.T) {
	model := NewModel(NewSourceBackend("repo", nil, "ary", &fakeSource{}), Palette{})
	model.Resize(160, 30)

	if model.Empty() {
		t.Error("a board reported itself empty before reading anything")
	}
	if out := strings.Join(model.Render(), "\n"); strings.Contains(out, "nothing is open") {
		t.Errorf("an unloaded board claimed a clean queue:\n%s", out)
	}
}

// Problems are heterogeneous: an unreadable spec, a failed reconcile, a dead
// run store, a whole source. Calling every one a spec misstates both what
// broke and how much of the board is missing.
func TestProblemsAreNotAllCalledSpecs(t *testing.T) {
	backend := NewSourceBackend("repo", nil, "ary", &fakeSource{
		specs:    []*feature.Spec{prCard("179", "board: ler a fila real", feature.Review)},
		problems: []error{errors.New("gh: could not resolve to a Repository")},
	})

	out := strings.Join(newTestModel(t, backend, 160, 30).Render(), "\n")
	if !strings.Contains(out, "1 problem(s)") {
		t.Errorf("the footer does not report the problem neutrally:\n%s", out)
	}
	if strings.Contains(out, "spec(s) could not be read") {
		t.Errorf("a dead source is still being called an unreadable spec:\n%s", out)
	}
}
