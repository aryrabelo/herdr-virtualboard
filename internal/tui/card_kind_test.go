package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

func kindSpec(id string, labels ...string) *feature.Spec {
	return &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: id, Title: "um titulo qualquer", Status: feature.Review, Labels: labels,
	}}
}

// The distinction the owner needs before anything else: what is waiting on an
// approval, and what is only waiting to be read.
func TestEveryCardSaysWhatItIs(t *testing.T) {
	cases := []struct {
		name string
		spec *feature.Spec
		want string
	}{
		{"a pull request", kindSpec("PR-162", LabelSourcePR, labelPRPrefix+"162"), kindPR},
		{"an issue", kindSpec("ceo-bora#150", LabelSourceIssue, labelIssuePrefix+"150"), kindIssue},
		{"a fio", kindSpec("FIO-3", LabelSourceFios), kindFio},
		{"a gate box", kindSpec("GATE-x-1", LabelSourceGates, labelGatePrefix+"x.md"), kindGate},
		// A VirtualBoard feature needs no word: every card is the same kind
		// there, so the tag would be noise on every row.
		{"a VirtualBoard feature", kindSpec("FTR-0007"), ""},
		{"nothing at all", nil, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := cardKind(testCase.spec); got != testCase.want {
				t.Errorf("cardKind = %q, quero %q", got, testCase.want)
			}
		})
	}
}

// Only the pull request is accented. A board where every kind shouts is a board
// where none does.
//
// The palette is a literal, not NewPalette(true): under `go test` stdout is not
// a terminal, so NewPalette disables colour and Accent == Dim == "". The first
// version of this test read the environment's palette and skipped itself, which
// a mutation run exposed — dropping the accent entirely left it green.
func TestOnlyThePullRequestIsAccented(t *testing.T) {
	model := &Model{palette: Palette{Accent: "<accent>", Dim: "<dim>"}}
	if got := model.cardKindColour(kindPR); got != "<accent>" {
		t.Errorf("PR pintado com %q, quero o accent", got)
	}
	for _, kind := range []string{kindIssue, kindFio, kindGate, ""} {
		if got := model.cardKindColour(kind); got != "<dim>" {
			t.Errorf("%q pintado com %q, quero dim", kind, got)
		}
	}
}

// A pull request points at the issue it closes; an issue card's own number is
// identity, not a link, and repeating it would tell the reader that #150
// closes #150.
func TestLinkedIssuesAreLinksNotIdentity(t *testing.T) {
	pr := kindSpec("PR-162", LabelSourcePR, labelPRPrefix+"162", labelIssuePrefix+"85")
	if got := cardLinkedIssuesLabel(pr); got != "Issue #85" {
		t.Errorf("links do PR = %q, quero \"Issue #85\"", got)
	}

	cross := kindSpec("PR-9", LabelSourcePR, labelPRPrefix+"9",
		labelIssuePrefix+"85", labelIssuePrefix+"aryrabelo/outro#12")
	got := cardLinkedIssuesLabel(cross)
	if !strings.Contains(got, "#85") || !strings.Contains(got, "aryrabelo/outro#12") {
		t.Errorf("links cruzados = %q, quero os dois refs", got)
	}
	if strings.Contains(got, "#aryrabelo/outro#12") {
		t.Errorf("ref cruzado ganhou um # a mais: %q", got)
	}

	issue := kindSpec("ceo-bora#150", LabelSourceIssue, labelIssuePrefix+"150")
	if got := cardLinkedIssuesLabel(issue); got != "" {
		t.Errorf("card de issue anunciou %q como link do proprio numero", got)
	}

	if got := cardLinkedIssuesLabel(kindSpec("FTR-1")); got != "" {
		t.Errorf("card sem link anunciou %q", got)
	}
}

// The tag has to reach the screen, not merely exist: both surfaces are pinned
// against rendered text so removing the call site reddens a test.
func TestTheKindReachesBothSurfaces(t *testing.T) {
	model := &Model{palette: NewPalette(false)}
	card := &Card{Spec: kindSpec("PR-162", LabelSourcePR, labelPRPrefix+"162",
		labelIssuePrefix+"85")}

	rows := model.renderCard(card, 60, false)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "("+kindPR+")") {
		t.Errorf("a linha do card nao diz o que o card e:\n%s", joined)
	}

	detail := strings.Join(model.detailLines(card, 80), "\n")
	if !strings.Contains(detail, "("+kindPR+")") {
		t.Errorf("o detalhe nao diz o que o card e:\n%s", detail)
	}
	if !strings.Contains(detail, "Issue #85") {
		t.Errorf("o detalhe nao mostra a issue que o PR fecha:\n%s", detail)
	}
}

// The card shape the owner approved, from the Factory board: who and what on
// the header, the title alone on its row, and only the labels a reader can
// act on as chips. The `hvb:*` tokens are bookkeeping and must not reach any
// row of the card, on either surface.
func TestTheCardReadsLikeTheApprovedOne(t *testing.T) {
	model := &Model{palette: NewPalette(false)}
	issue := kindSpec("ceo-bora#150", LabelSourceIssue, labelIssuePrefix+"150",
		LabelPrefix+"origin:usina", LabelPrefix+"rank:1", "rumo:grilling", "project:bugtoprompt")
	issue.Owner = "aryrabelo"
	issue.Updated = time.Now().Add(-26 * time.Hour).UTC().Format(time.RFC3339)

	rows := model.renderCard(&Card{Spec: issue}, 60, false)
	if len(rows) != cardHeight {
		t.Fatalf("card is %d rows, cardHeight says %d", len(rows), cardHeight)
	}
	if !strings.Contains(rows[0], "ceo-bora#150 (issue) · @aryrabelo · 1d") {
		t.Errorf("header does not say who and what: %q", rows[0])
	}
	if strings.Contains(rows[0], "Issue #150") {
		t.Errorf("an issue card links to itself: %q", rows[0])
	}
	if !strings.Contains(rows[1], "um titulo qualquer") || strings.Contains(rows[1], "#150") {
		t.Errorf("the title does not have its own row: %q", rows[1])
	}
	if !strings.Contains(rows[2], "rumo:grilling  project:bugtoprompt") {
		t.Errorf("the chips row lost the labels the reader acts on: %q", rows[2])
	}
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "hvb:") {
		t.Errorf("bookkeeping labels reached the card:\n%s", joined)
	}

	detail := strings.Join(model.detailLines(&Card{Spec: issue}, 100), "\n")
	if strings.Contains(detail, "hvb:") {
		t.Errorf("bookkeeping labels reached the detail:\n%s", detail)
	}
	if !strings.Contains(detail, "source") || !strings.Contains(detail, "usina") {
		t.Errorf("the detail does not say which binary answered:\n%s", detail)
	}
	if !strings.Contains(detail, "rumo:grilling project:bugtoprompt") {
		t.Errorf("the detail lost the labels the reader acts on:\n%s", detail)
	}
}
