package usinasrc

import (
	"strings"
	"testing"
)

// dispatchBody is the shape the usina prints, taken from the
// `.usina-despacho.json` this worktree was dispatched with (measured
// 2026-09-17) plus the `outcome` key the contract lists. `dry_run` is absent on
// purpose — a real dispatch does not print it, and TestDispatchArgvWithIssue
// reads that absence as false. The extra keys the real file carries are kept on
// purpose too: they must be ignored, not decoded.
const dispatchBody = `{"unidade": "linha-de-producao", "repo": "aryrabelo/herdr-virtualboard", ` +
	`"issue": "321", "workspace": "wFX", "pane": "wFX:p1", ` +
	`"worktree": "/Users/aryrabelo/Sites/bora-team/worktrees/herdr-virtualboard/linha-de-producao", ` +
	`"branch": "agente/linha-de-producao", "outcome": "dispatched", ` +
	`"repos": ["aryrabelo/ceo-bora"], "token_expira_em": ""}`

const dispatchKey = "usina agente dispatch"

// assertArgv compares an observed argument list element by element, which is
// the point of these two tests: a flag in the wrong position is a different
// command line even when the set of flags matches.
func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv com %d elementos, esperado %d:\n got: %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, esperado %q:\n got: %q\nwant: %q", i, got[i], want[i], got, want)
		}
	}
}

func TestDispatchArgvWithIssue(t *testing.T) {
	run := newFake(map[string]answer{dispatchKey: {stdout: dispatchBody}})

	result, err := Dispatch(run.run, DispatchRequest{
		Repo:       "aryrabelo/herdr-virtualboard",
		Unidade:    "linha-de-producao",
		Issue:      321,
		PromptFile: "/tmp/prompt.md",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	assertArgv(t, run.argsFor(t, dispatchKey), []string{
		"agente", "dispatch",
		"--repo", "aryrabelo/herdr-virtualboard",
		"--unidade", "linha-de-producao",
		"--issue", "321",
		"--prompt-file", "/tmp/prompt.md",
	})

	if result.Unidade != "linha-de-producao" || result.Issue != "321" {
		t.Errorf("unidade/issue lidos = %q/%q", result.Unidade, result.Issue)
	}
	if result.Branch != "agente/linha-de-producao" || result.Pane != "wFX:p1" {
		t.Errorf("branch/pane lidos = %q/%q", result.Branch, result.Pane)
	}
	if result.Outcome != "dispatched" {
		t.Errorf("outcome lido = %q, esperado dispatched", result.Outcome)
	}
	if result.DryRun {
		t.Error("dry_run ausente na saida foi lido como true")
	}
}

func TestDispatchArgvWithoutIssue(t *testing.T) {
	run := newFake(map[string]answer{dispatchKey: {stdout: dispatchBody}})

	if _, err := Dispatch(run.run, DispatchRequest{
		Repo:       "aryrabelo/herdr-virtualboard",
		Unidade:    "linha-de-producao",
		PromptFile: "/tmp/prompt.md",
	}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	assertArgv(t, run.argsFor(t, dispatchKey), []string{
		"agente", "dispatch",
		"--repo", "aryrabelo/herdr-virtualboard",
		"--unidade", "linha-de-producao",
		"--prompt-file", "/tmp/prompt.md",
	})
}

func TestDispatchDryRunTravelsAndWaivesThePromptFile(t *testing.T) {
	run := newFake(map[string]answer{dispatchKey: {stdout: `{"unidade": "linha-de-producao", "dry_run": true}`}})

	result, err := Dispatch(run.run, DispatchRequest{
		Repo:    "aryrabelo/herdr-virtualboard",
		Unidade: "linha-de-producao",
		Issue:   321,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("dry-run sem prompt-file devia ser aceito: %v", err)
	}
	if !result.DryRun {
		t.Error("dry_run: true na saida foi lido como false")
	}

	assertArgv(t, run.argsFor(t, dispatchKey), []string{
		"agente", "dispatch",
		"--repo", "aryrabelo/herdr-virtualboard",
		"--unidade", "linha-de-producao",
		"--issue", "321",
		"--dry-run",
	})

	// The same request without DryRun is the one the usina would refuse itself,
	// so hvb refuses it first and says which flag is missing.
	refused := newFake(nil)
	_, err = Dispatch(refused.run, DispatchRequest{
		Repo:    "aryrabelo/herdr-virtualboard",
		Unidade: "linha-de-producao",
		Issue:   321,
	})
	if err == nil {
		t.Fatal("despacho real sem prompt-file foi aceito")
	}
	if !strings.Contains(err.Error(), "--prompt-file") {
		t.Errorf("erro nao nomeia --prompt-file: %v", err)
	}
	if len(refused.calls) != 0 {
		t.Errorf("a usina foi chamada mesmo sem prompt-file: %v", refused.calls)
	}
}

func TestUnidadeFoldsAccentsCollapsesAndCutsAtWord(t *testing.T) {
	// The measured case: the title of ceo-bora#321, whose worktree the owner
	// named `linha-de-producao`. The issue number is the tail, so the name
	// still reads title-first in a `git branch` listing.
	if got := Unidade("linha de produção do hvb", 321); got != "linha-de-producao-do-hvb-321" {
		t.Errorf("Unidade(titulo medido) = %q, esperado linha-de-producao-do-hvb-321", got)
	}
	// Decomposed input folds the same way, so a title pasted from a source that
	// normalises differently does not produce a second worktree name.
	if got := Unidade("linha de produc\u0327a\u0303o do hvb", 321); got != "linha-de-producao-do-hvb-321" {
		t.Errorf("Unidade(titulo decomposto) = %q, esperado linha-de-producao-do-hvb-321", got)
	}
	if got := Unidade("  HVB: Ready, To --- Review!  ", 7); got != "hvb-ready-to-review-7" {
		t.Errorf("Unidade(pontuacao colapsada) = %q, esperado hvb-ready-to-review-7", got)
	}
	if got := Unidade("--- ??? !!! ---", 42); got != "issue-42" {
		t.Errorf("Unidade(so pontuacao) = %q, esperado issue-42", got)
	}
	if got := Unidade("日本語だけ", 43); got != "issue-43" {
		t.Errorf("Unidade(sem letra ASCII) = %q, esperado issue-43", got)
	}

	long := Unidade("a linha de producao do hvb passa a ler a fila real do dono sem adivinhar", 321)
	if len(long) > unidadeMax {
		t.Errorf("Unidade(titulo longo) = %q, %d caracteres, maximo %d", long, len(long), unidadeMax)
	}
	if strings.HasSuffix(long, "-") {
		t.Errorf("Unidade(titulo longo) = %q termina em hifen", long)
	}
	// The number comes out of the title's budget, not out of the limit: the
	// title is cut one word earlier than it used to be and the name is no
	// longer than before.
	if got := "a-linha-de-producao-do-hvb-passa-a-ler-a-321"; long != got {
		t.Errorf("Unidade(titulo longo) = %q, esperado %q (corte na palavra inteira)", long, got)
	}
}

// Two cards derive two units. The usina resolves BOTH the worktree
// (`worktrees/<repo>/<unidade>`) and the branch (`agente/<unidade>`) from the
// unit, so a unit shared by two cards dispatches the second agent into the
// first card's checkout — which is why the issue number is part of the name.
func TestUnidadeKeepsTwoCardsApartInsideTheLengthBudget(t *testing.T) {
	title := "ligar o board na linha"
	first, second := Unidade(title, 321), Unidade(title, 322)
	if first == second {
		t.Fatalf("as issues 321 e 322 derivaram a mesma unidade %q: o segundo despacho cairia na worktree do primeiro", first)
	}
	if !strings.HasSuffix(first, "-321") {
		t.Errorf("Unidade(%q, 321) = %q: o numero da issue nao aparece no nome", title, first)
	}

	// Titles that agree on their first 48 characters and differ only after the
	// cut are the harder case: the slug alone cannot tell them apart.
	head := "a linha de producao do hvb passa a ler a fila real do dono sem adivinhar"
	long, longer := Unidade(head+" agora", 11), Unidade(head+" depois", 12)
	if long == longer {
		t.Fatalf("dois titulos que só diferem depois do corte derivaram a mesma unidade %q", long)
	}

	// And every one of them still fits the budget a branch name has.
	for _, got := range []string{first, second, long, longer} {
		if len(got) > unidadeMax {
			t.Errorf("unidade %q tem %d caracteres, maximo %d", got, len(got), unidadeMax)
		}
		if strings.HasSuffix(got, "-") || strings.Contains(got, "--") {
			t.Errorf("unidade %q nao e um slug: hifen sobrando", got)
		}
	}

	// A number the card does not have adds no tail: "-0" would name an issue
	// that cannot exist.
	if got := Unidade(title, 0); got != "ligar-o-board-na-linha" {
		t.Errorf("Unidade(%q, 0) = %q, esperado o slug sem cauda", title, got)
	}
}

func TestDispatchRefusesMalformedRepoBeforeRunning(t *testing.T) {
	run := newFake(map[string]answer{dispatchKey: {stdout: dispatchBody}})

	for _, repo := range []string{"", "herdr-virtualboard", "aryrabelo/bora/hvb", "/hvb", "aryrabelo/"} {
		_, err := Dispatch(run.run, DispatchRequest{
			Repo:       repo,
			Unidade:    "linha-de-producao",
			PromptFile: "/tmp/prompt.md",
		})
		if err == nil {
			t.Errorf("repo %q foi aceito", repo)
			continue
		}
		if !strings.Contains(err.Error(), "owner/name") {
			t.Errorf("repo %q: erro nao ensina o conserto: %v", repo, err)
		}
	}

	if len(run.calls) != 0 {
		t.Fatalf("a usina foi chamada com repo malformado: %v", run.calls)
	}
}

func TestDispatchRefusesEmptyUnidadeBeforeRunning(t *testing.T) {
	run := newFake(map[string]answer{dispatchKey: {stdout: dispatchBody}})

	_, err := Dispatch(run.run, DispatchRequest{
		Repo:       "aryrabelo/herdr-virtualboard",
		Unidade:    "   ",
		PromptFile: "/tmp/prompt.md",
	})
	if err == nil {
		t.Fatal("unidade vazia foi aceita")
	}
	if !strings.Contains(err.Error(), "Unidade(") {
		t.Errorf("erro nao aponta como derivar a unidade: %v", err)
	}
	if len(run.calls) != 0 {
		t.Fatalf("a usina foi chamada sem unidade: %v", run.calls)
	}
}

func TestDispatchNamesUsinaOnUnreadableOutput(t *testing.T) {
	for _, unreadable := range []struct {
		name   string
		stdout string
	}{
		{name: "nao e JSON", stdout: "despachado com sucesso\n"},
		{name: "objeto sem unidade", stdout: `{"outcome": "dispatched"}`},
	} {
		run := newFake(map[string]answer{dispatchKey: {stdout: unreadable.stdout}})

		_, err := Dispatch(run.run, DispatchRequest{
			Repo:       "aryrabelo/herdr-virtualboard",
			Unidade:    "linha-de-producao",
			PromptFile: "/tmp/prompt.md",
		})
		if err == nil {
			t.Errorf("%s: saida foi aceita", unreadable.name)
			continue
		}
		if !strings.Contains(err.Error(), SourceUsina) {
			t.Errorf("%s: erro nao nomeia a usina: %v", unreadable.name, err)
		}
	}
}
