package fila

import "testing"

// TestHITLOutranksAssigneeBecauseTheOwnerAlreadyClaimedThoseSix pins the one
// ordering that is not obvious and that the real queue depends on: all six
// `hitl` issues in the owner's repo are assigned to him and are still blocked
// on him. If assignee won, the board would show his blockers as work in
// flight — exactly the state he opened the board to see.
func TestHITLOutranksAssigneeBecauseTheOwnerAlreadyClaimedThoseSix(t *testing.T) {
	got := ColumnFor(false, []string{"project:bugtoprompt", LabelHITL}, true, false)
	if got != Blocked {
		t.Fatalf("hitl com assignee deu %q; a precedencia manda Blocked (hitl ganha de assignee)", got)
	}
}

// TestClosedWinsOverEveryLabel: a finished HITL is finished. Nothing the labels
// say can pull a closed issue back out of Done.
func TestClosedWinsOverEveryLabel(t *testing.T) {
	got := ColumnFor(true, []string{LabelHITL, LabelGrilling}, true, true)
	if got != Done {
		t.Fatalf("issue fechada com hitl deu %q; closed decide antes de qualquer label: Done", got)
	}
}

// TestGrillingDecidesNoColumnAndTheStateDoes replaces
// TestGrillingOutranksAssignee, which asserted `rumo:grilling` -> Review and
// so pinned a defect as a feature: it guaranteed that a decision KIND kept
// deciding a lifecycle STATE. Measured on the owner's board 2026-09-18, that
// mapping put 7 of 33 cards in REVIEW, and 6 of those 7 had no pull request at
// all — nothing in this package reads a PR.
func TestGrillingDecidesNoColumnAndTheStateDoes(t *testing.T) {
	if got := ColumnFor(false, []string{LabelGrilling}, true, false); got != InProgress {
		t.Errorf("rumo:grilling com assignee deu %q; o estado decide, e ele tem dono: InProgress", got)
	}
	if got := ColumnFor(false, []string{LabelGrilling, "folha"}, false, false); got != Backlog {
		t.Errorf("rumo:grilling aberto e sem dono deu %q; era o caso dos 7 cards: Backlog", got)
	}
}

// TestNoLabelCanReachReviewBecauseReviewMeansAPullRequest is the invariant the
// change above buys, and it is the point of the whole fix: on an issue queue
// Review is unreachable, so the column means one thing only. It is filled by
// the pull-request source instead — ghboard maps an open PR to feature.Review
// (ghboard.go:332) — and a composing board therefore shows in REVIEW exactly
// what has a PR open. Any future label that wants a column has to name a
// STATE and be added here on purpose.
func TestNoLabelCanReachReviewBecauseReviewMeansAPullRequest(t *testing.T) {
	labels := []string{LabelHITL, LabelGrilling, "rumo:task", "rumo:map", "folha", "projeto"}
	for _, closed := range []bool{false, true} {
		for _, assigned := range []bool{false, true} {
			for _, blocked := range []bool{false, true} {
				for _, label := range labels {
					got := ColumnFor(closed, []string{label}, assigned, blocked)
					if got == Review {
						t.Errorf("label %q (closed=%v assigned=%v blocked=%v) alcancou Review; Review e' das PRs, nao de label de issue",
							label, closed, assigned, blocked)
					}
				}
				if got := ColumnFor(closed, labels, assigned, blocked); got == Review {
					t.Errorf("todas as labels juntas (closed=%v assigned=%v blocked=%v) alcancaram Review", closed, assigned, blocked)
				}
			}
		}
	}
}

// TestFrontierBlockedBlocksAnUnlabelledCard: the frontier is a second source of
// "blocked", independent of labels, and it must reach the board.
func TestFrontierBlockedBlocksAnUnlabelledCard(t *testing.T) {
	got := ColumnFor(false, []string{"rumo:task"}, false, true)
	if got != Blocked {
		t.Fatalf("card bloqueado pela fronteira deu %q; esperado Blocked", got)
	}
}

// TestPlainCardsFallThroughToTheirOwnColumn covers the two remaining exits of
// the ladder: nothing claimed is Backlog, merely claimed is InProgress.
func TestPlainCardsFallThroughToTheirOwnColumn(t *testing.T) {
	if got := ColumnFor(false, []string{"rumo:task"}, false, false); got != Backlog {
		t.Fatalf("card sem dono e sem bloqueio deu %q; esperado Backlog", got)
	}
	if got := ColumnFor(false, []string{"rumo:task"}, true, false); got != InProgress {
		t.Fatalf("card so com assignee deu %q; esperado InProgress", got)
	}
}

// TestLabelMatchIgnoresCaseAndSurroundingSpace: GitHub hands labels back as
// typed, and a card must not lose its column to a capital H.
func TestLabelMatchIgnoresCaseAndSurroundingSpace(t *testing.T) {
	if got := ColumnFor(false, []string{" HITL "}, false, false); got != Blocked {
		t.Fatalf("label \" HITL \" deu %q; casing e espaco sao ruido, esperado Blocked", got)
	}
}
