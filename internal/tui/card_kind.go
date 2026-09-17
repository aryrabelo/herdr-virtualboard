package tui

import (
	"strconv"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
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
