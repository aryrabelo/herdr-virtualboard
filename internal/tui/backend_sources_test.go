package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/linha"
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

// columnCanceled is the cancelled tail of a queue board. It is a column name,
// not a vb state: feature.Status is a string type, so a declared line can hold
// columns vb has never heard of.
const columnCanceled feature.Status = "canceled"

// queueWorkflow is the line a `hvb queue` board draws when nothing declared
// one: vb's five columns plus the cancelled tail, hidden while empty.
func queueWorkflow(t *testing.T) *feature.Workflow {
	t.Helper()
	cfg := config.Default()
	wf, err := cfg.BoardWorkflow()
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

// queueLine is the production line `hvb queue` runs when the repository
// declared none: the legacy tail column plus the one fact label that reaches
// it, read out of the same configuration the binary reads. A board given this
// classifies its own cards, which is the path production takes.
func queueLine(t *testing.T, wf *feature.Workflow) *Line {
	t.Helper()
	cfg := config.Default()
	when := map[feature.Status]string{}
	for name, label := range cfg.LineWhen() {
		when[feature.Status(name)] = label
	}
	if when[columnCanceled] == "" {
		t.Fatalf("the default line declares no fact for %q, so nothing would classify a cancelled pull request", columnCanceled)
	}
	return &Line{
		Policy:    linha.Policy{Order: wf.Columns(), When: when, Quiet: feature.Status(cfg.LineQuiet())},
		PRRepo:    "aryrabelo/bugtoprompt",
		IssueRepo: "aryrabelo/ceo-bora",
	}
}

// closedPR is a pull request as the SOURCE hands it over: closed without
// merging, still in the column internal/ghboard derived for it, carrying the
// fact label and nothing else decided. It only reaches the tail on a board
// with a line, so a test using it measures the classification rather than its
// own fixture.
func closedPR(number, title string, labels ...string) *feature.Spec {
	return prCard(number, title, feature.Review, append([]string{LabelCanceled}, labels...)...)
}

// canceledPR is the same pull request already classified, for the boards that
// have no line to classify it: `hvb queue --vault` reads FIOS.md, which has no
// pull request and no policy. Anything asserting how a card GETS to the tail
// must use closedPR and a line instead.
func canceledPR(number, title string, labels ...string) *feature.Spec {
	return prCard(number, title, columnCanceled, append([]string{LabelCanceled}, labels...)...)
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
	backend := NewSourceBackend("vault + repo", nil, nil, "ary", broken, working)

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

	backend := NewSourceBackend("vault", nil, nil, "ary", &fakeSource{specs: []*feature.Spec{
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
		}, []string{"PR-179", "--work-root"}},
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
//
// Built from the SOURCE's own column and classified by the line, because that
// is the path `hvb queue` takes: handing the board a card already in the tail
// would test the rendering of a status this file wrote itself.
func TestCancelledCardsGetTheirOwnReachableColumn(t *testing.T) {
	workflow := queueWorkflow(t)
	landed := prCard("178", "harness: fechar o gate", feature.Done, labelIssuePrefix+"167")
	dropped := closedPR("179", "board: abandonado", labelIssuePrefix+"168")
	if dropped.Status != feature.Review {
		t.Fatalf("the fixture already arrives in %q, so the line's classification is not being measured", dropped.Status)
	}
	backend := NewSourceBackend("repo", nil, workflow, "ary", &fakeSource{
		specs: []*feature.Spec{landed, dropped},
	}).WithLine(queueLine(t, workflow))
	model := newTestModel(t, backend, 160, 30)

	// The line is what moved it, and it says so where the detail pane looks.
	if dropped.Status != columnCanceled {
		t.Fatalf("the line left the closed pull request in %q", dropped.Status)
	}
	if want := LabelCanceled; !strings.Contains(dropped.Body, want) {
		t.Errorf("the card never says which fact put it in the tail:\n%s", dropped.Body)
	}
	counts := model.Counts()
	if counts[feature.Done] != 1 {
		t.Errorf("Done holds %d cards, want only the merged one", counts[feature.Done])
	}
	if counts[columnCanceled] != 1 {
		t.Errorf("the terminal column holds %d cards, want the cancelled one", counts[columnCanceled])
	}
	if cards := model.Cards(columnCanceled); len(cards) != 1 || cards[0].Spec.ID != "PR-179" {
		t.Fatalf("the cancelled column holds %+v", cards)
	}

	// Walk the cursor with the keys a user presses, from the leftmost column
	// to the far right, and stop where it stops.
	ctx := context.Background()
	model.column = 0
	for step := 0; step < 20; step++ {
		model.Handle(ctx, Key{Name: KeyRight})
	}
	if got := model.FocusedStatus(); got != columnCanceled {
		t.Fatalf("walking right ends on %q, so the cancelled column is unreachable", got)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "PR-179" {
		t.Fatalf("the cursor reached the column but not the card: %+v", card)
	}

	// And it is a real column on screen: counted in the header, and with its
	// card actually drawn in the body. The two are asserted apart because the
	// header abbreviates where the column title spells the name out, so a
	// single whole-screen match would let either one alone satisfy it.
	frame := model.Render()
	if !strings.Contains(frame[0], "CANCEL 1") {
		t.Errorf("the header does not count the cancelled column: %q", frame[0])
	}
	body := strings.Join(frame[1:], "\n")
	if !strings.Contains(body, "CANCELED 1") {
		t.Errorf("the board draws no cancelled column\n%s", body)
	}
	// By id and kind, not by title: a title is incidental to the column
	// width — 26 columns at 160/6 truncates one somewhere regardless — while
	// the id is what names the card and the kind marker is what says what it
	// is. Both have to reach the screen, and neither appears in the header,
	// which carries only "CANCEL 1".
	if !strings.Contains(body, "#179 (PR)") {
		t.Errorf("the cancelled card itself is not drawn\n%s", body)
	}
}

// Opening a board whose only work sits in the derived column must land the
// cursor there. Starting on an empty Backlog makes the user navigate before the
// board tells them anything.
func TestBoardOpensWhereTheWorkIsEvenWhenItIsCancelled(t *testing.T) {
	backend := NewSourceBackend("repo", nil, queueWorkflow(t), "ary", &fakeSource{specs: []*feature.Spec{
		canceledPR("179", "board: abandonado"),
	}})
	model := newTestModel(t, backend, 160, 30)

	if got := model.FocusedStatus(); got != columnCanceled {
		t.Fatalf("the board opened on %q, not on the only populated column", got)
	}
	if card := model.FocusedCard(); card == nil || card.Spec.ID != "PR-179" {
		t.Fatalf("the board opened on no card: %+v", card)
	}
}

// A pull request that closes unmerged while the user is looking at it changes
// column. The cursor must follow the card, which is the whole reason Reload
// re-finds the selection by identity instead of by index.
//
// Both loads go through the line, so what moves the card between them is the
// fact the source added — the same reclassification the running board does on
// its refresh tick.
func TestReloadFollowsACardIntoTheCancelledColumn(t *testing.T) {
	workflow := queueWorkflow(t)
	open := prCard("179", "board: ler a fila real", feature.Review, labelIssuePrefix+"168")
	source := &fakeSource{specs: []*feature.Spec{open}}
	backend := NewSourceBackend("repo", nil, workflow, "ary", source).WithLine(queueLine(t, workflow))
	model := newTestModel(t, backend, 160, 30)

	if got := model.FocusedStatus(); got != feature.Review {
		t.Fatalf("the board opened on %q, want review", got)
	}

	// The pull request is closed without merging: same source column, one
	// new fact.
	closed := closedPR("179", "board: ler a fila real", labelIssuePrefix+"168")
	source.specs = []*feature.Spec{closed}
	model.Reload(context.Background())
	if closed.Status != columnCanceled {
		t.Fatalf("the line left the closed pull request in %q", closed.Status)
	}
	if got := model.FocusedStatus(); got != columnCanceled {
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
	first := canceledPR("178", "primeiro abandonado")
	second := canceledPR("179", "segundo abandonado")
	source := &fakeSource{specs: []*feature.Spec{first, second}}
	model := newTestModel(t, NewSourceBackend("repo", nil, queueWorkflow(t), "ary", source), 160, 30)

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
// cancelled tail is as live as any other and must be counted.
//
// The board has to be a queue board for this to test anything: the spec board
// draws vb's five columns, so a card in the tail would be in no column at all
// and the assertions below would pass having measured nothing.
func TestActiveRunsInTheCancelledColumnAreCounted(t *testing.T) {
	card := spec("PR-7", "abandonado com agente vivo", columnCanceled, LabelSourcePR, LabelCanceled)
	backend := newFakeBackend(card)
	backend.wf = queueWorkflow(t)
	backend.runs = []*runs.Run{{
		ID: "r-1", FeatureID: "PR-7", Role: "qa", Kind: "claude",
		State: runs.Running, PaneID: "w1:p1", StartedAt: time.Now(),
	}}
	model := newTestModel(t, backend, 160, 30)

	// The guard against the guard: if this card were not in the tail,
	// everything below would pass without testing it.
	if got := len(model.Cards(columnCanceled)); got != 1 {
		t.Fatalf("the cancelled column holds %d cards, so this test proves nothing", got)
	}
	if got := model.ActiveRunCount(); got != 1 {
		t.Fatalf("the board counts %d active runs, want 1", got)
	}
	if frame := model.Render(); !strings.Contains(frame[0], "▶1") {
		t.Errorf("the header does not report the live run: %q", frame[0])
	}
}

// The compact layout replaces the columns with a breadcrumb. Every column has
// to appear in it, the cancelled tail included, or a user in a split pane
// cannot tell where they are.
func TestCompactBreadcrumbIncludesTheCancelledColumn(t *testing.T) {
	backend := NewSourceBackend("repo", nil, queueWorkflow(t), "ary", &fakeSource{specs: []*feature.Spec{
		prCard("178", "entregue", feature.Done),
		canceledPR("179", "board: abandonado"),
	}})
	// Narrow enough that only one column fits, which is what compact means.
	model := newTestModel(t, backend, 43, 30)
	if !model.compact() {
		t.Fatal("43 columns fits more than one board column, so this is not the compact layout")
	}

	ctx := context.Background()
	model.column = 0
	for range 20 {
		model.Handle(ctx, Key{Name: KeyRight})
	}
	if got := model.FocusedStatus(); got != columnCanceled {
		t.Fatalf("walking right in the narrow layout ends on %q", got)
	}

	// Frame line 0 is the header; line 1 is the breadcrumb.
	crumbs := model.Render()[1]
	if !strings.Contains(crumbs, "CANCEL") {
		t.Errorf("the breadcrumb omits the column the cursor is on: %q", crumbs)
	}
	if !strings.Contains(crumbs, "DONE") {
		t.Errorf("the breadcrumb lost the lifecycle: %q", crumbs)
	}
}

// A queue board with nothing cancelled is exactly the five-column VirtualBoard
// lifecycle. The tail is declared — it is in the workflow either way — and has
// to stay off the screen until something lands in it.
func TestTheDerivedColumnStaysHiddenWhenEmpty(t *testing.T) {
	backend := newFakeBackend(spec("FTR-0001", "First thing", feature.Done))
	backend.wf = queueWorkflow(t)
	model := newTestModel(t, backend, 160, 30)

	if strings.Contains(screen(model), "CANCEL") {
		t.Error("a board with nothing cancelled grew a sixth column")
	}
	order := model.columnOrder()
	if len(order) != 5 {
		t.Fatalf("the board draws %v, want vb's five columns alone", order)
	}
	for index, want := range feature.VB().Columns() {
		if order[index] != want {
			t.Errorf("column %d is %q, want %q", index, order[index], want)
		}
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
// Asserted on the card at 100 columns rather than on a whole frame, because no
// frame gives a card that much: the compact layout is one column only when
// fewer than two fit, so its widest card is 43 columns, and a windowed board
// divides the width by however many columns fit. A 31-column card truncates
// the meta row exactly as it already does for a long role name, so a
// whole-screen assertion would be measuring the truncation, not the facts.
func TestCardShowsThePullRequestFacts(t *testing.T) {
	backend := NewSourceBackend("repo", nil, nil, "ary", &fakeSource{specs: []*feature.Spec{
		prCard("179", "board: ler a fila real", feature.Review,
			labelIssuePrefix+"168", LabelExternal, LabelDraft),
	}})
	model := newTestModel(t, backend, 160, 30)
	card := model.FocusedCard()
	if card == nil {
		t.Fatal("the board opened on no card")
	}

	out := strings.Join(model.renderCard(card, 100, false), "\n")
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
	vbSpec.Updated = time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339)

	model := newTestModel(t, newFakeBackend(vbSpec), 160, 30)
	card := model.Cards(feature.Backlog)[0]
	header := model.renderCard(card, 60, false)[0]
	if strings.Contains(header, "1h") {
		t.Errorf("a vb card grew an age: %q", header)
	}
	if extra := model.sourceMeta(card); len(extra) != 0 {
		t.Errorf("a vb card grew %v", extra)
	}

	// The same card, once a source claims it, does show its age — in the
	// header, next to the owner, where the Factory card puts it.
	vbSpec.Labels = []string{LabelSourceFios}
	header = model.renderCard(card, 60, false)[0]
	if !strings.Contains(header, "@ary · 1h") {
		t.Errorf("a source card header is %q, want the owner and its age", header)
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

// The cancelled tail is not the drawer for whatever did not fit. The board
// draws the column the card arrived in and does not go looking for a reason to
// move it, so a done card stays in Done whatever its labels say — including a
// `hvb:state:canceled` on a card from another source, which the line policy
// refuses to read as a cancelled pull request.
func TestTheTerminalColumnIsNotACatchAll(t *testing.T) {
	backend := NewSourceBackend("repo", nil, queueWorkflow(t), "ary", &fakeSource{specs: []*feature.Spec{
		spec("GATE-x-1", "resolvido de outra forma", feature.Done, LabelSourceGates, "resolved:other"),
		spec("FIO-9", "entregue", feature.Done, LabelSourceFios, "closed", "stale"),
		spec("FIO-10", "fio com label de outro mundo", feature.Done, LabelSourceFios, LabelCanceled),
		canceledPR("179", "fechado sem merge"),
	}})
	model := newTestModel(t, backend, 160, 30)

	if got := model.Counts()[feature.Done]; got != 3 {
		t.Errorf("Done holds %d cards, want the three that are merely done", got)
	}
	cards := model.Cards(columnCanceled)
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
	dropped := canceledPR("179", "fechado sem merge", LabelSourceFios, LabelSourceGates)
	backend := NewSourceBackend("repo", nil, queueWorkflow(t), "ary", &fakeSource{
		specs: []*feature.Spec{merged, dropped},
	})
	model := newTestModel(t, backend, 160, 30)

	// The merged one stays shipped; the dead one stays dead.
	if got := model.Counts()[feature.Done]; got != 1 {
		t.Errorf("Done holds %d cards, want the merged pull request", got)
	}
	cards := model.Cards(columnCanceled)
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
	failed := NewSourceBackend("repo", nil, nil, "ary",
		&fakeSource{problems: []error{errors.New("gh: could not resolve to a Repository")}})
	clean := NewSourceBackend("repo", nil, nil, "ary", &fakeSource{})

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
	model := newTestModel(t, NewSourceBackend("repo", nil, nil, "ary", &fakeSource{}), 160, 30)
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
	model := NewModel(NewSourceBackend("repo", nil, nil, "ary", &fakeSource{}), Palette{})
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
	backend := NewSourceBackend("repo", nil, nil, "ary", &fakeSource{
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
