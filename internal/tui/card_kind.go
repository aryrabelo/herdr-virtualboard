package tui

import (
	"strconv"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/fila"
)

// A card's kind is presentation, never identity.
//
// The obvious move is to mint it into the id — `ceo-bora#150 (issue)` — and it
// is wrong: feature.Spec.ID is a key. It indexes SourceBackend.origins, it is
// lowercased into runs.NewID, and runs.TabLabel turns it into a real Herdr tab
// name. A parenthesised id with a space would travel into tab labels and run
// ids to buy a word on screen.
//
// So the kind is derived where it is drawn, from the labels the sources already
// mint. It matters because a board mixing pull requests with issues gives the
// reader no way to tell what is waiting for an approval from what is merely
// waiting to be looked at, which is the one distinction the owner needs before
// anything else on the card.
const (
	kindPR    = "PR"
	kindIssue = "issue"
	kindFio   = "fio"
	kindGate  = "gate"
)

// cardKind is the short word for what a card is, or empty for a VirtualBoard
// feature — where every card is the same kind and the word would be noise.
func cardKind(spec *feature.Spec) string {
	switch {
	case spec == nil:
		return ""
	case spec.HasLabel(LabelSourcePR):
		return kindPR
	case spec.HasLabel(LabelSourceIssue):
		return kindIssue
	case spec.HasLabel(LabelSourceFios):
		return kindFio
	case spec.HasLabel(LabelSourceGates):
		return kindGate
	default:
		return ""
	}
}

// cardKindColour makes the pull requests findable at a glance. Only one kind is
// accented, on purpose: a board where every kind shouts is a board where none
// does, and a pull request is the only card that can be waiting on the reader's
// approval. Everything else is dim.
func (m *Model) cardKindColour(kind string) string {
	if kind == kindPR {
		return m.palette.Accent
	}
	return m.palette.Dim
}

// cardLinkedIssues are the issues a card points AT, which is not the same as
// the issue a card IS.
//
// internal/ghboard mints one `hvb:issue:` label per closing issue of a pull
// request, so those are genuine links. internal/issuesrc mints the same label
// for the issue's OWN number, so on an issue card the label is identity and
// repeating it as a link would tell the reader that #150 closes #150.
func cardLinkedIssues(spec *feature.Spec) []string {
	if spec == nil || cardKind(spec) == kindIssue {
		return nil
	}
	var refs []string
	for _, label := range spec.Labels {
		if !strings.HasPrefix(label, labelIssuePrefix) {
			continue
		}
		if ref := strings.TrimSpace(strings.TrimPrefix(label, labelIssuePrefix)); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

// cardLinkedIssuesLabel is the links rendered as one field value: "Issue #85",
// or "Issue #85, aryrabelo/other#12" when a pull request closes issues in more
// than one repository. A bare number gets the `#`; a cross-repository ref
// already carries its own.
func cardLinkedIssuesLabel(spec *feature.Spec) string {
	refs := cardLinkedIssues(spec)
	if len(refs) == 0 {
		return ""
	}
	shown := make([]string, 0, len(refs))
	for _, ref := range refs {
		if _, err := strconv.Atoi(ref); err == nil {
			ref = "#" + ref
		}
		shown = append(shown, ref)
	}
	return "Issue " + strings.Join(shown, ", ")
}

// visibleLabels are the labels a reader can act on: everything the sources
// did not mint for themselves. `hvb:*` is the board's own bookkeeping — which
// source built the card, which binary answered, what it links to — and it is
// already rendered where it means something (the kind tag, the closes field,
// the source field). Listing the raw tokens next to `rumo:grilling` made the
// owner read six labels to find the two that were his.
func visibleLabels(spec *feature.Spec) []string {
	if spec == nil {
		return nil
	}
	var out []string
	for _, label := range spec.Labels {
		if strings.HasPrefix(label, LabelPrefix) {
			continue
		}
		out = append(out, label)
	}
	return out
}

// cardSource names the binary that answered for an issue card, off the
// `hvb:origin:` label issuesrc mints (usina or gh). It is shown so a reader
// can tell a usina-ranked queue from a degraded gh listing, which is the
// difference between "kit.py ordered this" and "nobody did".
func cardSource(spec *feature.Spec) string {
	return labelValue(spec, LabelPrefix+"origin:")
}

// A card waiting on the owner's own hands is not a card waiting on work.
//
// `fila.LabelHITL` is the one label that says so, and it is the only spelling
// of it in the tree: fila.ColumnFor routes it to Blocked ahead of an assignee
// and internal/roles refuses every implementer charter for it. The board is
// the surface where that fact has to be readable, because BLOCKED holds two
// populations with opposite next actions — a `hitl` card needs an answer from
// the owner, a frontier-blocked card needs another task to land — and until
// now they rendered identically: the label sat dim in the middle of the chip
// row, between `project:bugtoprompt` and the rest.
//
// Measured on the owner's queue (2026-09-18): all 6 BLOCKED cards are `hitl`,
// so the two populations have never been on screen together. The marking is
// therefore keyed on the label, not on the column, and a card the frontier
// blocked gets nothing — which is exactly how they read apart once kit.py
// starts blocking cards.
//
// The label does reach here: internal/issuesrc copies every repository label
// that is not `hvb:`-prefixed onto the spec (issuesrc.go, spec()), so `hitl`
// arrives verbatim in Spec.Labels. The frontier's blocked set does not — it is
// consumed by fila.ColumnFor and never minted as a label — so "blocked by
// another task" is only ever the absence of this one.
const (
	// needsAnswerGlyph is U+25C6 BLACK DIAMOND. The owner's terminal font
	// is ~/Library/Fonts/JetBrainsMonoNerdFontMono-Regular.ttf, and its
	// cmap maps U+25C6 to glyph 1252 (measured by parsing the table:
	// U+25CB is glyph 1257 and U+25D0 is absent in the same block, so
	// coverage is per codepoint, never per block).
	needsAnswerGlyph = "◆"
	// needsAnswerBadge leads the chip row, so truncation at a 26-column
	// card cannot eat it, and it carries a word: a colour alone is a
	// legend the reader has to have been told.
	needsAnswerBadge = needsAnswerGlyph + " NEEDS YOU"
)

// cardNeedsAnswer reports whether a card is waiting on the owner himself.
//
// A finished card is not waiting on anybody: fila.ColumnFor settles `closed`
// before any label, so a `hitl` card that reached Done is one the owner
// already unblocked, and a cancelled pull request is nobody's answer to give.
//
// Both of those are now one question — is this a column work does not leave —
// asked of the line rather than of a hardcoded pair. The derived cancelled
// tail stopped being a constant this package mints: it is a declared column
// (`config.legacyTailDef`) a `when = "hvb:state:canceled"` label pins cards
// into, so the spec's own status already IS the column and the old
// `boardStatus` remapping has nothing left to remap. A declared column with no
// `next` is exactly what `done` and `canceled` have in common, and it is why
// the check is the workflow's terminality instead of two names.
//
// `Has` guards the terminality, because `Terminal` answers true for a column
// the workflow never declared — its `next` list is the zero value. Those
// columns do reach the board (columnOrder appends them, so an undeclared
// status is visible rather than swallowed), and reading them as terminal would
// silently strip the mark off the one population that most needs it: a card
// sitting in a column the line forgot to declare.
func (m *Model) cardNeedsAnswer(spec *feature.Spec) bool {
	if spec == nil {
		return false
	}
	if m.workflow != nil && m.workflow.Has(spec.Status) && m.workflow.Terminal(spec.Status) {
		return false
	}
	for _, label := range spec.Labels {
		if labelIsHITL(label) {
			return true
		}
	}
	return false
}

// labelIsHITL compares the way fila.ColumnFor and roles.Suggest both compare:
// surrounding space and casing are noise. A card labelled `HITL` routes to the
// unblocker and must not render as an ordinary blocked card.
func labelIsHITL(label string) bool {
	return strings.EqualFold(strings.TrimSpace(label), fila.LabelHITL)
}
