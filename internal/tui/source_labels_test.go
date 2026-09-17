package tui

// The imports below are TEST-ONLY and must stay that way. internal/tui has no
// production dependency on internal/fios or internal/ghboard, and that is not
// an oversight for someone to tidy up later.
//
// The board declares its own Source interface (backend_sources.go) precisely so
// that composing a board costs nothing in dependencies: a source satisfies it
// structurally. Importing a source package into production code would undo
// that, and the cost was measured while this was written — both source packages
// were mid-rewrite and red for minutes at a time, and internal/tui kept
// compiling, with its tests still meaningful, only because it does not import
// them.
//
// Reusing a sibling's exported constant in production code would buy one
// spelling at that price. This test buys the same guarantee for free: it reads
// the sources' own constants, so a rename on either side fails here instead of
// quietly emptying a column or dropping a badge.
import (
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/fios"
	"github.com/virtualboard/herdr-virtualboard/internal/ghboard"
	"github.com/virtualboard/herdr-virtualboard/internal/issuesrc"
)

// The board spells the source labels itself so that composing a board does not
// depend on which sources exist. That is a deliberate second spelling, and the
// only thing that makes it safe is this test.
func TestLabelSpellingsMatchTheSources(t *testing.T) {
	cases := []struct {
		name         string
		board, owner string
	}{
		{"the reserved prefix", LabelPrefix, ghboard.LabelPrefix},
		{"the reserved prefix", LabelPrefix, fios.LabelPrefix},
		{"source:fios", LabelSourceFios, fios.LabelSourceFios},
		{"source:gates", LabelSourceGates, fios.LabelSourceGates},
		{"gate:", labelGatePrefix, fios.LabelGatePrefix},
		{"source:pr", LabelSourcePR, ghboard.LabelSourcePR},
		{"state:canceled", LabelCanceled, ghboard.LabelCanceled},
		{"external", LabelExternal, ghboard.LabelExternal},
		{"draft", LabelDraft, ghboard.LabelDraft},
		{"the reserved prefix", LabelPrefix, issuesrc.LabelPrefix},
		{"source:issue", LabelSourceIssue, issuesrc.LabelSourceIssue},
		// The issue source and the pull-request source must agree on the
		// issue key, or a pull request and the issue it lands would be two
		// unrelated cards.
		{"issue:", labelIssuePrefix, issuesrc.LabelIssuePrefix},
	}
	for _, testCase := range cases {
		if testCase.board != testCase.owner {
			t.Errorf("%s: the board reads %q but the source writes %q",
				testCase.name, testCase.board, testCase.owner)
		}
	}

	// Every label the board derives a decision from must live in the reserved
	// namespace. An unprefixed one is a name a repository can own, and the
	// board would be reading the repository's opinion as the harness's verdict.
	for _, label := range []string{LabelSourceFios, LabelSourceGates, LabelSourcePR,
		LabelSourceIssue, LabelCanceled, LabelExternal, LabelDraft,
		labelIssuePrefix, labelPRPrefix, labelGatePrefix,
		issuesrc.LabelOriginUsina, issuesrc.LabelOriginGH, issuesrc.LabelRankPrefix} {
		if !strings.HasPrefix(label, LabelPrefix) {
			t.Errorf("%q is outside the reserved namespace, so repository content can forge it", label)
		}
	}

	// A `- [-]` gate box is resolved another way, not cancelled. If it ever
	// reused the cancelled label the terminal column would start mixing
	// "this died" with "this was solved differently".
	if fios.LabelResolvedOther == LabelCanceled {
		t.Errorf("a resolved-another-way box now carries %q, which is the cancelled column's label",
			LabelCanceled)
	}
}
