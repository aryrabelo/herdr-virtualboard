package linha

import (
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

const repo = "aryrabelo/bugtoprompt"

// declaredOrder is the line from the contract: twelve columns, six of which
// GitHub does not know.
var declaredOrder = []feature.Status{
	"intake", "triage", "planning", "building", "first-review", "fr-approved",
	"pr-processing", "pr-verification", "ready-to-review", "ready-to-merge",
	"done", "canceled",
}

func line() Policy {
	return Policy{
		Order: declaredOrder,
		When: map[feature.Status]string{
			"pr-verification": LabelStateOpen,
			"done":            "hvb:state:merged",
			"canceled":        "hvb:state:canceled",
		},
		Quiet: "ready-to-review",
	}
}

func activity(when time.Time) string {
	return LabelActivityPrefix + when.UTC().Format(time.RFC3339)
}

// openPR is a green open pull request whose last measured activity is `quiet`
// ago, on a repository with a 30m timer. The green fact is spelled through the
// constant because it is a precondition of advancing, not decoration.
func openPR(now time.Time, quiet time.Duration) Input {
	return Input{
		Policy:   line(),
		Labels:   []string{"hvb:source:pr", "hvb:pr:64", LabelStateOpen, LabelCheckGreen, activity(now.Add(-quiet))},
		Source:   "pr-processing",
		Repo:     repo,
		Timer:    30 * time.Minute,
		TimerSet: true,
		Now:      now,
	}
}

// The literal acceptance criterion of ceo-bora#321: a merged pull request
// renders in the merged column even when the store says otherwise, and the
// reason carries both the fact and what the store said.
func TestMergedPullRequestBeatsTheStoredColumn(t *testing.T) {
	got := Resolve(Input{
		Policy: line(),
		Labels: []string{"hvb:source:pr", "hvb:pr:64", "hvb:state:merged"},
		Source: "pr-processing",
		Stored: "ready-to-review",
		Repo:   repo,
		Now:    time.Now(),
	})
	if got.Column != "done" {
		t.Fatalf("coluna = %q, quero done (o store não pode esconder um merge)", got.Column)
	}
	if !got.Pinned {
		t.Errorf("Pinned = false, um fato do GitHub pina a coluna")
	}
	if !strings.Contains(got.Reason, "fato do GitHub: hvb:state:merged") {
		t.Errorf("motivo %q não nomeia o fato", got.Reason)
	}
	if !strings.Contains(got.Reason, "o store dizia ready-to-review") {
		t.Errorf("motivo %q não relata a discordância do store", got.Reason)
	}
}

func TestRepoWithoutTimerHoldsTheOpenPullRequest(t *testing.T) {
	now := time.Now()
	in := openPR(now, 3*time.Hour)
	in.Timer, in.TimerSet = 0, false

	got := Resolve(in)
	if got.Column != "pr-verification" {
		t.Fatalf("coluna = %q, quero pr-verification: sem timer não há avanço", got.Column)
	}
	if want := "timer não configurado para " + repo; got.Reason != want {
		t.Errorf("motivo = %q, quero exatamente %q", got.Reason, want)
	}
	if !got.Pinned {
		t.Errorf("Pinned = false, a PR aberta continua sendo fato do GitHub")
	}
}

func TestQuietGreenPullRequestAdvancesOnlyAfterTheTimer(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	advanced := Resolve(openPR(now, 31*time.Minute))
	if advanced.Column != "ready-to-review" {
		t.Fatalf("coluna = %q, quero ready-to-review após 31m com timer de 30m", advanced.Column)
	}
	want := "verde e quieto por 31m0s (timer 30m0s do repo " + repo + ")"
	if advanced.Reason != want {
		t.Errorf("motivo = %q, quero %q", advanced.Reason, want)
	}

	// The boundary itself advances: the contract is `quieto >= timer`.
	if exact := Resolve(openPR(now, 30*time.Minute)); exact.Column != "ready-to-review" {
		t.Errorf("coluna = %q com quieto == timer, quero ready-to-review", exact.Column)
	}

	short := Resolve(openPR(now, 30*time.Minute-time.Second))
	if short.Column != "pr-verification" {
		t.Fatalf("coluna = %q, quero pr-verification: falta 1s para o timer", short.Column)
	}
	if want := "quieto há 29m59s, faltam 1s de 30m0s"; short.Reason != want {
		t.Errorf("motivo = %q, quero %q", short.Reason, want)
	}
}

func TestRedCheckHoldsThePullRequestAfterTheQuietWindow(t *testing.T) {
	now := time.Now()
	in := openPR(now, 3*time.Hour)
	in.Labels = append(in.Labels, LabelCheckRed)

	got := Resolve(in)
	if got.Column != "pr-verification" {
		t.Fatalf("coluna = %q, quero pr-verification: check vermelho não avança", got.Column)
	}
	if want := "check vermelho: retrocesso a building disponível"; got.Reason != want {
		t.Errorf("motivo = %q, quero %q", got.Reason, want)
	}
}

// The refusal the timer exists to make: absence of red is not green. A pull
// request whose checks are queued, expected, or never reported at all carries
// no verdict label, and no amount of silence turns that into success — the
// card would otherwise reach review having been tested by nobody. The second
// row is the same absence with a lookalike a repository CAN own: only the
// reserved spelling counts as the forge's word.
func TestQuietPullRequestWithoutAGreenCheckHolds(t *testing.T) {
	now := time.Now()
	const want = "sem check verde: o avanço exige sucesso medido, não ausência de vermelho"

	for _, testCase := range []struct {
		name  string
		extra string // what the card carries in place of the green fact
	}{
		{"nenhum check reportado", ""},
		{"rótulo do repositório fora do namespace reservado", "check:green"},
	} {
		in := openPR(now, 3*time.Hour)
		in.Labels = []string{"hvb:source:pr", "hvb:pr:64", LabelStateOpen, activity(now.Add(-3 * time.Hour))}
		if testCase.extra != "" {
			in.Labels = append(in.Labels, testCase.extra)
		}

		got := Resolve(in)
		if got.Column != "pr-verification" {
			t.Errorf("%s: coluna = %q, quero pr-verification: sem verde não há avanço", testCase.name, got.Column)
		}
		if got.Reason != want {
			t.Errorf("%s: motivo = %q, quero %q", testCase.name, got.Reason, want)
		}
		if !got.Pinned {
			t.Errorf("%s: Pinned = false, a PR aberta continua sendo fato do GitHub", testCase.name)
		}
	}
}

func TestOpenPullRequestWithoutMeasuredActivityDoesNotAdvance(t *testing.T) {
	in := openPR(time.Now(), 3*time.Hour)
	in.Labels = []string{LabelStateOpen, "hvb:check:green", LabelActivityPrefix + "ontem"}

	got := Resolve(in)
	if got.Column != "pr-verification" {
		t.Fatalf("coluna = %q, quero pr-verification: ausência de medição não é silêncio", got.Column)
	}
	if want := "sem atividade medida na PR"; got.Reason != want {
		t.Errorf("motivo = %q, quero %q", got.Reason, want)
	}
}

// A line that declared no quiet column cannot advance anything on a timer; the
// open pull request is just a fact.
func TestLineWithoutQuietColumnKeepsThePlainFact(t *testing.T) {
	in := openPR(time.Now(), 3*time.Hour)
	in.Policy.Quiet = ""

	got := Resolve(in)
	if got.Column != "pr-verification" {
		t.Fatalf("coluna = %q, quero pr-verification", got.Column)
	}
	if want := "fato do GitHub: " + LabelStateOpen; got.Reason != want {
		t.Errorf("motivo = %q, quero %q", got.Reason, want)
	}
}

func TestStoreOverridesTheSourceOnlyForDeclaredColumns(t *testing.T) {
	in := Input{
		Policy: line(),
		Labels: []string{"hvb:source:issue", "bug"},
		Source: "intake",
		Stored: "planning",
		Now:    time.Now(),
	}

	got := Resolve(in)
	if got.Column != "planning" {
		t.Fatalf("coluna = %q, quero planning: sem fato, o store manda", got.Column)
	}
	if want := "coluna do store do hvb"; got.Reason != want {
		t.Errorf("motivo = %q, quero %q", got.Reason, want)
	}
	if got.Pinned {
		t.Errorf("Pinned = true, o store não pina nada")
	}

	in.Stored = "limbo"
	stale := Resolve(in)
	if stale.Column != "intake" {
		t.Fatalf("coluna = %q, quero intake: coluna que a linha não declara é ignorada", stale.Column)
	}
	if stale.Reason != "" {
		t.Errorf("motivo = %q, quero vazio quando a fonte decide", stale.Reason)
	}
}

// Two matching facts on one card: the DECLARED order decides, not map
// iteration, so the answer must flip when the declaration flips.
func TestWhenLadderFollowsDeclaredOrder(t *testing.T) {
	labels := []string{"hvb:state:merged", "hvb:state:canceled"}

	first := Resolve(Input{Policy: line(), Labels: labels, Source: "pr-processing", Now: time.Now()})
	if first.Column != "done" {
		t.Fatalf("coluna = %q, quero done: done é declarada antes de canceled", first.Column)
	}

	flipped := line()
	flipped.Order = append([]feature.Status{}, declaredOrder[:10]...)
	flipped.Order = append(flipped.Order, "canceled", "done")
	second := Resolve(Input{Policy: flipped, Labels: labels, Source: "pr-processing", Now: time.Now()})
	if second.Column != "canceled" {
		t.Fatalf("coluna = %q, quero canceled quando ela é declarada primeiro", second.Column)
	}
}

// A reason a human reads must not carry sub-second noise like 29m59.7231s. The
// fraction comes from the clock, never from the label: `hvb:activity:` is
// RFC3339, which is whole seconds, so `Now` is the only sub-second source and
// this test has to carry one for the rounding to be exercised at all.
//
// Each row asserts the WHOLE sentence and the column, not just the absence of
// a dot: a truncated, empty or plainly wrong reason reads as rounded too.
func TestReasonsRoundDurationsToTheSecond(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 723_000_000, time.UTC)
	for _, testCase := range []struct {
		quiet  time.Duration
		column feature.Status
		reason string
	}{
		// 31m past a stamp the label truncated to the second: 31m0.723s,
		// which the reason rounds UP to the whole second above it.
		{31 * time.Minute, "ready-to-review", "verde e quieto por 31m1s (timer 30m0s do repo " + repo + ")"},
		// 29m59.723s: still short of the window, and the 277ms left of it
		// round to 0s rather than being printed as a fraction. The card is
		// held by the comparison, never by the rendered number.
		{29*time.Minute + 59*time.Second, "pr-verification", "quieto há 30m0s, faltam 0s de 30m0s"},
	} {
		got := Resolve(openPR(now, testCase.quiet))
		if got.Column != testCase.column {
			t.Errorf("quieto por %s: coluna = %q, quero %q", testCase.quiet, got.Column, testCase.column)
		}
		if got.Reason != testCase.reason {
			t.Errorf("quieto por %s: motivo = %q, quero exatamente %q", testCase.quiet, got.Reason, testCase.reason)
		}
	}
}

func TestParseActivitySkipsAnUnreadableStamp(t *testing.T) {
	want := time.Date(2026, 9, 17, 11, 30, 0, 0, time.UTC)
	got, ok := ParseActivity([]string{"hvb:check:green", LabelActivityPrefix + "17/09/2026", " " + activity(want)})
	if !ok {
		t.Fatalf("ParseActivity não leu o rótulo legível depois do ilegível")
	}
	if !got.Equal(want) {
		t.Errorf("atividade = %s, quero %s", got, want)
	}

	if _, ok := ParseActivity([]string{LabelActivityPrefix, "hvb:state:open"}); ok {
		t.Errorf("prefixo sem valor não é atividade medida")
	}
}
