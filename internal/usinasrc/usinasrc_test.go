package usinasrc

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Fixtures are the bytes the binaries actually printed, escapes included:
// routeWithRefusal is `usina rota show --repo aryrabelo/ceo-bora` verbatim
// (measured 2026-09-17), and usinaListBody is two elements of
// `usina issue list --repo aryrabelo/ceo-bora --label project:bugtoprompt`.
const (
	routeClean = `{"repo": "aryrabelo/ceo-bora", "mapa": "aryrabelo/ceo-bora", ` +
		`"spec": "aryrabelo/ceo-bora", "issues": "aryrabelo/ceo-bora", "config": "ceo-bora.yml"}`

	routeWithRefusal = `{"repo": "aryrabelo/ceo-bora", "mapa": "aryrabelo/ceo-bora", ` +
		`"spec": "aryrabelo/ceo-bora", "issues": "aryrabelo/ceo-bora", ` +
		`"kb": {"recusa": "'kb' ausente ou n\u00e3o-mapping em usina-config (ceo-bora.yml): ` +
		`None \u2014 declare kb: {repo: <owner/repo>, modo: proposta|direto}"}, "config": "ceo-bora.yml"}`

	// refusalPhrase is routeWithRefusal's phrase as the owner reads it, so the
	// test also proves the escapes are decoded rather than carried raw.
	refusalPhrase = "'kb' ausente ou não-mapping em usina-config (ceo-bora.yml): " +
		"None — declare kb: {repo: <owner/repo>, modo: proposta|direto}"

	routeIssuesElsewhere = `{"repo": "aryrabelo/ceo-bora", "mapa": "aryrabelo/ceo-bora", ` +
		`"issues": "aryrabelo/fila-de-verdade", "config": "ceo-bora.yml"}`

	usinaListBody = `[{"assignees": [{"id": "MDQ6VXNlcjEwNDYwODc=", "login": "aryrabelo", ` +
		`"name": "Ary Rabelo", "databaseId": 1046087}], ` +
		`"labels": [{"id": "LA_kwDOT2S-iM8AAAACzz-N-w", "name": "project:bugtoprompt", ` +
		`"description": "Slug do projeto do atlas", "color": "ededed"}, ` +
		`{"id": "LA_kwDOT2S-iM8AAAAC0NhmOQ", "name": "hitl", ` +
		`"description": "Bloqueio que s\u00f3 o dono resolve", "color": "D93F0B"}], ` +
		`"number": 160, "state": "OPEN", ` +
		`"title": "HITL: testar o Pro de ponta a ponta na extens\u00e3o real (ceo-bora#132)", ` +
		`"url": "https://github.com/aryrabelo/ceo-bora/issues/160"},` +
		`{"assignees": [], "labels": [{"name": "rumo:task"}], "number": 141, "state": "OPEN", ` +
		`"title": "Kanban le a fila real do dono", ` +
		`"url": "https://github.com/aryrabelo/ceo-bora/issues/141"}]`
)

const (
	ceoRepo = "aryrabelo/ceo-bora"
	label   = "project:bugtoprompt"
	limit   = 30
)

// call is one invocation the fake observed, kept so a test can prove which
// repository was actually asked rather than only which one was reported.
type call struct {
	name string
	args []string
}

// answer is what the fake replies to one scripted command.
type answer struct {
	stdout string
	err    error
}

// fake is the injected Runner. It scripts replies by binary plus the two
// leading verbs — "usina rota show", "usina issue list", "gh issue list" — and
// fails any unscripted call, so a test can never pass by accident of a command
// nobody meant to allow.
type fake struct {
	answers map[string]answer
	calls   []call
}

func newFake(answers map[string]answer) *fake {
	return &fake{answers: answers}
}

func (f *fake) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name: name, args: append([]string(nil), args...)})

	key, ok := commandKey(name, args)
	if !ok {
		return nil, fmt.Errorf("fake: chamada sem verbos: %s %v", name, args)
	}
	reply, ok := f.answers[key]
	if !ok {
		return nil, fmt.Errorf("fake: chamada nao roteirizada: %s", key)
	}
	if reply.err != nil {
		return nil, reply.err
	}
	return []byte(reply.stdout), nil
}

func commandKey(name string, args []string) (string, bool) {
	if len(args) < 2 {
		return "", false
	}
	return name + " " + args[0] + " " + args[1], true
}

// argsFor returns the arguments of the single call matching key.
func (f *fake) argsFor(t *testing.T, key string) []string {
	t.Helper()
	var found []string
	for _, observed := range f.calls {
		if got, ok := commandKey(observed.name, observed.args); ok && got == key {
			if found != nil {
				t.Fatalf("%q foi chamado mais de uma vez: %v", key, f.calls)
			}
			found = observed.args
		}
	}
	if found == nil {
		t.Fatalf("%q nunca foi chamado; chamadas: %v", key, f.calls)
	}
	return found
}

// flagValue reads the value of flag in an observed argument list.
func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, arg := range args {
		if arg == flag {
			if i+1 >= len(args) {
				t.Fatalf("flag %s veio sem valor em %v", flag, args)
			}
			return args[i+1]
		}
	}
	t.Fatalf("flag %s ausente em %v", flag, args)
	return ""
}

func TestLoadFromUsinaLeavesReasonEmpty(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeClean},
		"usina issue list": {stdout: usinaListBody},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if queue.Origin.Source != SourceUsina {
		t.Errorf("Origin.Source = %q, quero %q", queue.Origin.Source, SourceUsina)
	}
	if queue.Origin.Reason != "" {
		t.Errorf("usina respondeu tudo, Reason devia ser vazio, veio %q", queue.Origin.Reason)
	}
	if queue.RouteRefusal != "" {
		t.Errorf("rota nao recusou nada, RouteRefusal = %q", queue.RouteRefusal)
	}
	if queue.Repo != ceoRepo {
		t.Errorf("Repo = %q, quero %q", queue.Repo, ceoRepo)
	}
	if len(queue.Cards) != 2 {
		t.Fatalf("cards = %d, quero 2: %+v", len(queue.Cards), queue.Cards)
	}

	first := queue.Cards[0]
	if first.Number != 160 || first.Closed {
		t.Errorf("primeiro card = {%d closed=%v}, quero {160 closed=false}", first.Number, first.Closed)
	}
	if first.URL != "https://github.com/aryrabelo/ceo-bora/issues/160" {
		t.Errorf("URL = %q", first.URL)
	}
	if !strings.HasPrefix(first.Title, "HITL: testar o Pro de ponta a ponta na extensão real") {
		t.Errorf("Title = %q", first.Title)
	}
	if got := strings.Join(first.Labels, ","); got != "project:bugtoprompt,hitl" {
		t.Errorf("Labels = %q, quero \"project:bugtoprompt,hitl\"", got)
	}
	if got := strings.Join(first.Assignees, ","); got != "aryrabelo" {
		t.Errorf("Assignees = %q, quero \"aryrabelo\"", got)
	}
}

func TestLoadCarriesRouteRefusalAndStillLists(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeWithRefusal},
		"usina issue list": {stdout: usinaListBody},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err != nil {
		t.Fatalf("recusa de uma sub-area nao invalida a rota, mas Load falhou: %v", err)
	}
	if queue.RouteRefusal != refusalPhrase {
		t.Errorf("RouteRefusal = %q, quero a frase da rota %q", queue.RouteRefusal, refusalPhrase)
	}
	if len(queue.Cards) != 2 {
		t.Errorf("a fila devia ter sido lida apesar da recusa, cards = %d", len(queue.Cards))
	}
	if queue.Origin.Source != SourceUsina || queue.Origin.Reason != "" {
		t.Errorf("recusa e dado, nao degradacao: Origin = %+v", queue.Origin)
	}
}

func TestLoadFallsBackToCeoRepoWithNamedReasonWhenRouteDies(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show":  {err: errors.New("usina saiu 2: instancia da usina indeterminada")},
		"usina issue list": {stdout: usinaListBody},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if queue.Repo != ceoRepo {
		t.Errorf("rota morta devia cair no proprio CEO: Repo = %q", queue.Repo)
	}
	if got := flagValue(t, run.argsFor(t, "usina issue list"), "--repo"); got != ceoRepo {
		t.Errorf("issue list pediu --repo %q, quero %q", got, ceoRepo)
	}
	if queue.Origin.Reason == "" {
		t.Fatal("rota morta e degradacao silenciosa: Origin.Reason ficou vazio")
	}
	for _, want := range []string{"rota show", "instancia da usina indeterminada", ceoRepo} {
		if !strings.Contains(queue.Origin.Reason, want) {
			t.Errorf("Origin.Reason = %q, nao nomeia %q", queue.Origin.Reason, want)
		}
	}
}

func TestLoadListsFromRouteResolvedRepoNotCeoRepo(t *testing.T) {
	const routed = "aryrabelo/fila-de-verdade"
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeIssuesElsewhere},
		"usina issue list": {stdout: usinaListBody},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if queue.Repo != routed {
		t.Errorf("Repo = %q, quero o repo que a rota resolveu (%q)", queue.Repo, routed)
	}
	args := run.argsFor(t, "usina issue list")
	if got := flagValue(t, args, "--repo"); got != routed {
		t.Errorf("issue list pediu --repo %q, quero %q — a rota manda, nao o ceoRepo", got, routed)
	}
	if got := flagValue(t, args, "--label"); got != label {
		t.Errorf("issue list pediu --label %q, quero %q", got, label)
	}
	if got := flagValue(t, args, "--limit"); got != "30" {
		t.Errorf("issue list pediu --limit %q, quero \"30\"", got)
	}
}

// Both sources must be asked the same question, or the Done column silently
// depends on which one answered. Measured 2026-09-17: `usina issue list`
// without --state returns only OPEN (30 of the 32 issues carrying
// project:bugtoprompt), while the gh degradation forces --state all.
func TestBothSourcesAskForEveryStateSoDoneDoesNotDependOnWhoAnswered(t *testing.T) {
	usina := newFake(map[string]answer{
		"usina rota show":  {stdout: routeIssuesElsewhere},
		"usina issue list": {stdout: usinaListBody},
	})
	if _, err := Load(usina.run, ceoRepo, label, limit); err != nil {
		t.Fatalf("Load via usina: %v", err)
	}
	if got := flagValue(t, usina.argsFor(t, "usina issue list"), "--state"); got != "all" {
		t.Errorf("usina pediu --state %q, quero \"all\": sem isso a usina devolve so OPEN e o gh devolve tudo", got)
	}

	gh := newFake(map[string]answer{
		"usina rota show":  {stdout: routeIssuesElsewhere},
		"usina issue list": {err: errors.New("boom")},
		"gh issue list":    {stdout: usinaListBody},
	})
	if _, err := Load(gh.run, ceoRepo, label, limit); err != nil {
		t.Fatalf("Load via gh: %v", err)
	}
	if got := flagValue(t, gh.argsFor(t, "gh issue list"), "--state"); got != "all" {
		t.Errorf("gh pediu --state %q, quero \"all\"", got)
	}
}

func TestLoadDegradesToGHWithNamedReason(t *testing.T) {
	const ghBody = `[{"number": 141, "title": "Kanban le a fila real", ` +
		`"url": "https://github.com/aryrabelo/ceo-bora/issues/141", "state": "OPEN", ` +
		`"labels": [{"name": "rumo:task"}], "assignees": [{"login": "aryrabelo"}]}]`
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeClean},
		"usina issue list": {err: errors.New("usina saiu 2: issue list nao roteia este repo")},
		"gh issue list":    {stdout: ghBody},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if queue.Origin.Source != SourceGH {
		t.Errorf("Origin.Source = %q, quero %q", queue.Origin.Source, SourceGH)
	}
	if queue.Origin.Reason == "" {
		t.Fatal("degradacao muda: Origin.Reason ficou vazio quando a fila veio do gh")
	}
	for _, want := range []string{"usina issue list", "issue list nao roteia este repo", "gh"} {
		if !strings.Contains(queue.Origin.Reason, want) {
			t.Errorf("Origin.Reason = %q, nao nomeia %q", queue.Origin.Reason, want)
		}
	}
	if len(queue.Cards) != 1 || queue.Cards[0].Number != 141 {
		t.Fatalf("cards do gh = %+v", queue.Cards)
	}

	args := run.argsFor(t, "gh issue list")
	if got := flagValue(t, args, "--state"); got != "all" {
		t.Errorf("gh pediu --state %q, quero \"all\"", got)
	}
	if got := flagValue(t, args, "--json"); got != ghFields {
		t.Errorf("gh pediu --json %q, quero %q", got, ghFields)
	}
}

func TestLoadErrorsWhenNeitherSourceAnswers(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeClean},
		"usina issue list": {err: errors.New("usina nao esta no PATH")},
		"gh issue list":    {err: errors.New("gh saiu 4: not logged in")},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err == nil {
		t.Fatal("as duas fontes morreram e Load devolveu fila silenciosa")
	}
	for _, want := range []string{"usina nao esta no PATH", "not logged in", ceoRepo} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("erro = %q, nao nomeia %q", err.Error(), want)
		}
	}
	if queue.Cards != nil {
		t.Errorf("ninguem respondeu, mas vieram cards: %+v", queue.Cards)
	}
	if queue.Origin.Source != "" {
		t.Errorf("Origin.Source = %q, quero vazio: ninguem serviu a lista", queue.Origin.Source)
	}
	if queue.Origin.Reason == "" {
		t.Error("Origin.Reason ficou vazio com as duas fontes mortas")
	}
}

func TestLoadReadsClosedStateInBothSpellings(t *testing.T) {
	const body = `[{"number": 1, "state": "CLOSED", "title": "grita"},` +
		`{"number": 2, "state": "closed", "title": "sussurra"},` +
		`{"number": 3, "state": "OPEN", "title": "aberta"},` +
		`{"number": 4, "state": "open", "title": "aberta baixa"}]`
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeClean},
		"usina issue list": {stdout: body},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[int]bool{1: true, 2: true, 3: false, 4: false}
	if len(queue.Cards) != len(want) {
		t.Fatalf("cards = %d, quero %d", len(queue.Cards), len(want))
	}
	for _, card := range queue.Cards {
		if card.Closed != want[card.Number] {
			t.Errorf("issue %d: Closed = %v, quero %v", card.Number, card.Closed, want[card.Number])
		}
	}
}

func TestLoadGivesCardWithoutAssigneesEmptySlices(t *testing.T) {
	const body = `[{"number": 7, "state": "OPEN", "title": "orfa", "assignees": []},` +
		`{"number": 8, "state": "OPEN", "title": "sem as chaves"}]`
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeClean},
		"usina issue list": {stdout: body},
	})

	queue, err := Load(run.run, ceoRepo, label, limit)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, card := range queue.Cards {
		if card.Assignees == nil {
			t.Errorf("issue %d: Assignees nil, quero slice vazio", card.Number)
		}
		if card.Labels == nil {
			t.Errorf("issue %d: Labels nil, quero slice vazio", card.Number)
		}
		// A consumer ranges over both on every repaint; nil would only be
		// safe by accident of Go, not by contract.
		for _, assignee := range card.Assignees {
			t.Errorf("issue %d: assignee inventado %q", card.Number, assignee)
		}
		for _, name := range card.Labels {
			t.Errorf("issue %d: label inventada %q", card.Number, name)
		}
	}
}

func TestLoadRefusesInventedStateRatherThanAssumingOpen(t *testing.T) {
	const body = `[{"number": 9, "title": "sem lifecycle"}]`
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeClean},
		"usina issue list": {stdout: body},
		"gh issue list":    {stdout: body},
	})

	_, err := Load(run.run, ceoRepo, label, limit)
	if err == nil {
		t.Fatal("issue sem 'state' virou card aberto: Closed false nao foi medido")
	}
	if !strings.Contains(err.Error(), "sem 'state'") {
		t.Errorf("erro = %q, nao nomeia o campo ausente", err.Error())
	}
}

func TestLoadRejectsMisWiredCall(t *testing.T) {
	run := newFake(map[string]answer{"usina rota show": {stdout: routeClean}})

	cases := []struct {
		name  string
		run   Runner
		repo  string
		limit int
	}{
		{name: "sem runner", run: nil, repo: ceoRepo, limit: limit},
		{name: "sem repo", run: run.run, repo: "  ", limit: limit},
		{name: "limite zero", run: run.run, repo: ceoRepo, limit: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := Load(testCase.run, testCase.repo, label, testCase.limit); err == nil {
				t.Fatal("chamada mal-fiada aceita")
			}
		})
	}
	if len(run.calls) != 0 {
		t.Errorf("chamada mal-fiada executou binario: %v", run.calls)
	}
}
