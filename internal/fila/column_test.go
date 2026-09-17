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

// TestGrillingOutranksAssignee: a decision under examination is under review
// even when someone owns it.
func TestGrillingOutranksAssignee(t *testing.T) {
	got := ColumnFor(false, []string{LabelGrilling}, true, false)
	if got != Review {
		t.Fatalf("rumo:grilling com assignee deu %q; grilling decide antes de assignee: Review", got)
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
