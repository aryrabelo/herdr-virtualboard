package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/colunas"
	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/ghboard"
	"github.com/virtualboard/herdr-virtualboard/internal/linha"
)

// lineTOML is the full line of ceo-bora#321 with its facts and its timer, as a
// repository declares it. workflow_test.go declares a shorter one for the
// columns alone; this one is what the facts and the store are measured against. It is spelled as TOML
// rather than built as a struct so the test exercises the same path a user
// does: a misparsed key would produce a board with the right columns and no
// facts, which is the failure this whole file exists to catch.
const lineTOML = `
[workflow]
columns = ["intake", "triage", "planning", "building", "first-review",
           "fr-approved", "pr-processing", "pr-verification",
           "ready-to-review", "ready-to-merge", "done", "canceled"]

[columns.intake]
next = ["triage", "canceled"]

[columns.triage]
next = ["planning", "building", "canceled"]

[columns.planning]
next = ["building"]

[columns.building]
next = ["first-review"]

[columns."first-review"]
next = ["building", "fr-approved"]

[columns."fr-approved"]
next = ["pr-processing"]

[columns."pr-processing"]
next = ["pr-verification"]

[columns."pr-verification"]
title = "PR Verification"
when = "hvb:state:open"
next = ["building", "ready-to-review"]

[columns."ready-to-review"]
title = "Ready To Review"
gate = "human"
quiet = true
next = ["ready-to-merge", "building"]

[columns."ready-to-merge"]
next = ["done"]

[columns.done]
when = "hvb:state:merged"

[columns.canceled]
when = "hvb:state:canceled"

[repos."aryrabelo/bugtoprompt"]
quiet_timer = "30m"
`

// lineConfig loads declaredLine the way `hvb queue` does.
func lineConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.ProjectFile), []byte(lineTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HVB_CONFIG", filepath.Join(dir, "absent-global.toml"))
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("the declared line does not load: %v", err)
	}
	return cfg
}

// openColumnStore opens the line's store under dir. The directory is a
// parameter rather than a fresh t.TempDir() because a restart is two stores
// over the SAME file, and that is exactly what one of these tests measures.
func openColumnStore(t *testing.T, dir string) *colunas.Store {
	t.Helper()
	store, err := colunas.Open(dir, colunas.BoardID("aryrabelo/ceo-bora"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// lineBoard assembles the same pair internal/cli does: the declared workflow
// and the line built from the same configuration.
func lineBoard(t *testing.T, store *colunas.Store, now time.Time, sources ...Source) (*SourceBackend, *feature.Workflow) {
	t.Helper()
	cfg := lineConfig(t)
	wf, err := cfg.BoardWorkflow()
	if err != nil {
		t.Fatal(err)
	}
	when := map[feature.Status]string{}
	for name, label := range cfg.LineWhen() {
		when[feature.Status(name)] = label
	}
	backend := NewSourceBackend("ceo-bora issues", cfg, wf, "ary", sources...).WithLine(&Line{
		Policy: linha.Policy{
			Order: wf.Columns(),
			When:  when,
			Quiet: feature.Status(cfg.LineQuiet()),
		},
		Store:     store,
		Timer:     cfg.QuietTimer,
		PRRepo:    "aryrabelo/bugtoprompt",
		IssueRepo: "aryrabelo/ceo-bora",
		Now:       func() time.Time { return now },
	})
	return backend, wf
}

// issueCard is a card as internal/issuesrc reports it: the column is the
// source's own guess and the line is free to raise it.
func issueCard(number string, status feature.Status, labels ...string) *feature.Spec {
	card := spec("ceo-bora#"+number, "ligar o board na linha", status,
		append([]string{LabelSourceIssue, labelIssuePrefix + number}, labels...)...)
	card.Owner = ""
	return card
}

// The literal acceptance criterion: a pull request merged on GitHub renders in
// Done even though the store says Ready To Review, and the reason says so where
// a reader looks.
func TestAMergedPullRequestBeatsTheStoredColumnOnTheBoard(t *testing.T) {
	store := openColumnStore(t, t.TempDir())
	if err := store.Set(colunas.Entry{ID: "PR-162", Column: "ready-to-review"}); err != nil {
		t.Fatal(err)
	}
	merged := prCard("162", "linha: ligar o store", "review", ghboard.LabelMerged)
	backend, _ := lineBoard(t, store, time.Now(), &fakeSource{specs: []*feature.Spec{merged}})

	specs, _, problems := backend.Load(context.Background())
	if len(problems) != 0 {
		t.Fatalf("a healthy line reported problems: %v", problems)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d cards, want 1", len(specs))
	}
	if specs[0].Status != "done" {
		t.Errorf("the merged pull request is in %q, want done — the store outvoted GitHub", specs[0].Status)
	}
	for _, want := range []string{"fato do GitHub", ghboard.LabelMerged, "o store dizia ready-to-review"} {
		if !strings.Contains(specs[0].Body, want) {
			t.Errorf("the card detail never says %q:\n%s", want, specs[0].Body)
		}
	}
}

// The other literal criterion: a repository with no declared quiet window holds
// its pull request in PR Verification and says exactly why. A silent default
// here would advance somebody's pull request with nobody's say-so.
func TestAPullRequestOfAnUndeclaredRepositoryHoldsWithTheReason(t *testing.T) {
	green := prCard("163", "linha: timer por repo", "review",
		ghboard.LabelOpen, ghboard.LabelCheckGreen,
		ghboard.LabelActivityPrefix+"2026-09-17T10:00:00Z")
	now := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)

	// Same card, same four hours of silence, on a board whose pull-request
	// source reads a repository the file never mentions.
	backend, _ := lineBoard(t, nil, now, &fakeSource{specs: []*feature.Spec{green}})
	backend.line.PRRepo = "aryrabelo/herdr-virtualboard"

	specs, _, _ := backend.Load(context.Background())
	if specs[0].Status != "pr-verification" {
		t.Errorf("the card advanced to %q with no timer declared for its repository", specs[0].Status)
	}
	if want := "timer não configurado para aryrabelo/herdr-virtualboard"; !strings.Contains(specs[0].Body, want) {
		t.Errorf("the card detail never says %q:\n%s", want, specs[0].Body)
	}

	// The declared repository, unchanged in every other respect, does advance.
	declared, _ := lineBoard(t, nil, now, &fakeSource{specs: []*feature.Spec{
		prCard("163", "linha: timer por repo", "review",
			ghboard.LabelOpen, ghboard.LabelCheckGreen,
			ghboard.LabelActivityPrefix+"2026-09-17T10:00:00Z"),
	}})
	advanced, _, _ := declared.Load(context.Background())
	if advanced[0].Status != "ready-to-review" {
		t.Errorf("a repository declaring quiet_timer = 30m held its card in %q after four quiet hours",
			advanced[0].Status)
	}
}

// The store's whole purpose: a column GitHub has no field for survives a
// restart. "Reiniciar o hvb" is a second backend over the same file, which is
// what a restart is.
func TestAMoveIntoTriageSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store := openColumnStore(t, dir)
	card := issueCard("321", "intake")
	backend, _ := lineBoard(t, store, time.Now(), &fakeSource{specs: []*feature.Spec{card}})

	if _, _, problems := backend.Load(context.Background()); len(problems) != 0 {
		t.Fatalf("load reported problems: %v", problems)
	}
	if err := backend.Move(context.Background(), "ceo-bora#321", "triage", "ary"); err != nil {
		t.Fatalf("moving an issue to a column GitHub has no fact for: %v", err)
	}

	restarted, _ := lineBoard(t, openColumnStore(t, dir), time.Now(), &fakeSource{
		specs: []*feature.Spec{issueCard("321", "intake")},
	})
	specs, _, _ := restarted.Load(context.Background())
	if specs[0].Status != "triage" {
		t.Fatalf("after a restart the card is in %q, want triage", specs[0].Status)
	}
	if want := "coluna do store do hvb"; !strings.Contains(specs[0].Body, want) {
		t.Errorf("the card detail never says where the column came from:\n%s", specs[0].Body)
	}

	// And the store holds the move, keyed so `hvb state show` finds it.
	stored, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("the store holds %d entries, want 1", len(stored))
	}
	switch {
	case stored[0].Column != "triage":
		t.Errorf("the stored column is %q, want triage", stored[0].Column)
	case stored[0].Issue != 321:
		t.Errorf("the stored issue number is %d, want 321 — read off the card's label", stored[0].Issue)
	case stored[0].Repo != "aryrabelo/ceo-bora":
		t.Errorf("the stored repository is %q, want the issue queue's", stored[0].Repo)
	}
}

// A column a fact decides cannot be written. Storing it would be a lie with a
// timestamp: the next Load reads the fact and overrules the entry, so the board
// would show a move that visibly undoes itself.
func TestTheBoardRefusesToStoreAColumnAFactDecides(t *testing.T) {
	store := openColumnStore(t, t.TempDir())
	backend, _ := lineBoard(t, store, time.Now(), &fakeSource{
		specs: []*feature.Spec{issueCard("321", "intake")},
	})
	backend.Load(context.Background())

	for _, column := range []feature.Status{"done", "canceled", "pr-verification"} {
		err := backend.Move(context.Background(), "ceo-bora#321", column, "ary")
		if err == nil {
			t.Fatalf("the board stored %q, which only the forge decides", column)
		}
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("moving to %q refused without saying the board is read-only for GitHub: %v", column, err)
		}
	}
	stored, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("a refused move still wrote %d entries", len(stored))
	}
}

// The board refuses a transition the line does not declare. The picker greys
// those out, but H/L reaches Move without asking it.
func TestTheBoardRefusesATransitionTheLineDoesNotDeclare(t *testing.T) {
	backend, _ := lineBoard(t, openColumnStore(t, t.TempDir()), time.Now(), &fakeSource{
		specs: []*feature.Spec{issueCard("321", "intake")},
	})
	backend.Load(context.Background())

	err := backend.Move(context.Background(), "ceo-bora#321", "ready-to-merge", "ary")
	if err == nil {
		t.Fatal("intake jumped straight to ready-to-merge")
	}
	if !strings.Contains(err.Error(), "the line does not allow intake → ready-to-merge") {
		t.Errorf("the refusal does not name the transition: %v", err)
	}
	if !strings.Contains(err.Error(), "triage") {
		t.Errorf("the refusal does not say where the card may go instead: %v", err)
	}
}

// One despatcher per card. An issue is dispatched by `usina agente dispatch`,
// never by the run store's despatcher — which claims through vb, and vb has
// never heard of an issue number. Checked on the backend rather than only on
// the keystroke, because offerDispatch reaches Dispatch without the picker.
func TestAnIssueCardNeverReachesTheRunDespatcher(t *testing.T) {
	backend, _ := lineBoard(t, openColumnStore(t, t.TempDir()), time.Now())
	backend.line.Issues = &fakeIssueDespatcher{sentence: "usina dispatched ceo-bora#321 to wFX:p1 on agente/linha"}
	card := issueCard("321", "building")

	if _, err := backend.Dispatch(context.Background(), card, "backend_dev", "", false); err == nil {
		t.Error("an issue card reached dispatch.Dispatcher through Dispatch")
	} else if !strings.Contains(err.Error(), "usina agente dispatch") {
		t.Errorf("the refusal does not name the despatcher that owns the card: %v", err)
	}
	yes := true
	if _, err := backend.DispatchWith(context.Background(), card, "backend_dev", "", &yes, &yes); err == nil {
		t.Error("an issue card reached dispatch.Dispatcher through DispatchWith")
	}

	// A pull request on the same board still uses the run despatcher, which
	// is behaviour this change was not asked to delete. With no dispatcher
	// wired, that shows as the board's own missing-flags refusal.
	pr := prCard("162", "linha", "pr-verification")
	if _, err := backend.Dispatch(context.Background(), pr, "qa", "", false); err == nil {
		t.Error("a board with no dispatcher dispatched a pull request")
	} else if strings.Contains(err.Error(), "usina agente dispatch") {
		t.Errorf("a pull request was sent to the issue despatcher: %v", err)
	}

	sentence, err := backend.DispatchIssue(context.Background(), card)
	if err != nil {
		t.Fatalf("the issue card could not be dispatched through the line: %v", err)
	}
	if !strings.Contains(sentence, "wFX:p1") {
		t.Errorf("the board shows %q, which does not name the pane to watch", sentence)
	}
}

// A board with no line is the board this backend shipped with: every column
// comes from its source and every write is refused. `hvb queue --vault` is
// exactly that, so the two shapes are tested apart.
func TestABoardWithoutALineStillRefusesEveryMove(t *testing.T) {
	card := fiosCard("FIO-3", "Ligar o board", feature.Backlog, "/vault/FIOS.md")
	backend := NewSourceBackend("vault", nil, nil, "ary", &fakeSource{specs: []*feature.Spec{card}})
	backend.Load(context.Background())

	err := backend.Move(context.Background(), "FIO-3", feature.InProgress, "ary")
	if err == nil {
		t.Fatal("a board with no line accepted a move")
	}
	if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "FIOS.md") {
		t.Errorf("the refusal does not name the file that owns the card: %v", err)
	}
}

// An unreadable store costs the overrides, not the board. Every fact is still
// measured and every card still draws, and the problem is reported rather than
// absorbed.
func TestAnUnreadableColumnStoreStillDrawsTheFacts(t *testing.T) {
	dir := t.TempDir()
	// A directory where the store expects a file: os.ReadFile fails with
	// something that is not os.IsNotExist, which is the one case load does
	// not treat as an empty store.
	store, err := colunas.Open(dir, "queue-broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	merged := prCard("162", "linha", "review", ghboard.LabelMerged)
	backend, _ := lineBoard(t, store, time.Now(), &fakeSource{specs: []*feature.Spec{merged}})

	specs, _, problems := backend.Load(context.Background())
	if len(specs) != 1 || specs[0].Status != "done" {
		t.Fatalf("a dead store lost the measured fact: %+v", specs)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "stored columns unread") {
		t.Fatalf("the dead store was absorbed rather than reported: %v", problems)
	}
}

// fakeIssueDespatcher stands in for the usina.
type fakeIssueDespatcher struct {
	sentence string
	calls    int
}

func (f *fakeIssueDespatcher) DispatchIssue(context.Context, *feature.Spec) (string, error) {
	f.calls++
	return f.sentence, nil
}
