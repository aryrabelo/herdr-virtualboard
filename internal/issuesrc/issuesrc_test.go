package issuesrc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/fila"
)

// Every byte below was measured against aryrabelo/ceo-bora on 2026-09-17: the
// route, one HITL issue (#160), one unassigned task, and kit.py's frontier for
// the bugtoprompt project. No test here executes usina, gh or python3.
const (
	routeBody = `{"repo": "aryrabelo/ceo-bora", "mapa": "aryrabelo/ceo-bora", ` +
		`"issues": "aryrabelo/ceo-bora"}`

	listBody = `[{"number": 160, "title": "Mintar credencial da factory", ` +
		`"url": "https://github.com/aryrabelo/ceo-bora/issues/160", "state": "OPEN", ` +
		`"labels": [{"name": "hitl"}, {"name": "project:bugtoprompt"}], ` +
		`"assignees": [{"login": "aryrabelo"}]},` +
		`{"number": 31, "title": "CI vermelho no deploy", ` +
		`"url": "https://github.com/aryrabelo/ceo-bora/issues/31", "state": "OPEN", ` +
		`"labels": [{"name": "rumo:task"}], "assignees": []},` +
		`{"number": 12, "title": "Fechar o rumo do landing", ` +
		`"url": "https://github.com/aryrabelo/ceo-bora/issues/12", "state": "CLOSED", ` +
		`"labels": [{"name": "rumo:task"}], "assignees": []}]`

	frontierBody = `{"pronto": [{"titulo": "CI vermelho no deploy", "numero": 31, ` +
		`"rank": 0, "porque": "alinhamento: receita"}], "bloqueado": []}`
)

const (
	ceoRepo = "aryrabelo/ceo-bora"
	label   = "project:bugtoprompt"
	kitPath = "/Users/aryrabelo/Sites/bora-team/ceo-bora/bin/kit.py"
	slug    = "bugtoprompt"
)

type answer struct {
	stdout string
	err    error
}

// fake answers by command prefix, and records what it was asked.
type fake struct {
	answers map[string]answer
	calls   []string
}

func newFake(answers map[string]answer) *fake {
	return &fake{answers: answers}
}

func (f *fake) run(name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	for prefix, reply := range f.answers {
		if strings.HasPrefix(line, prefix) {
			if reply.err != nil {
				return []byte(reply.stdout), reply.err
			}
			return []byte(reply.stdout), nil
		}
	}
	return nil, errors.New("fake: nobody answers " + line)
}

func (f *fake) asked(prefix string) bool {
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func healthy() *fake {
	return newFake(map[string]answer{
		"usina rota show":    {stdout: routeBody},
		"usina issue list":   {stdout: listBody},
		"python3 " + kitPath: {stdout: frontierBody},
	})
}

func load(t *testing.T, run *fake, kit, project string) ([]*feature.Spec, []error) {
	t.Helper()
	specs, errs := New(run.run, ceoRepo, label, kit, project, ceoRepo, 30).Load(context.Background())
	return specs, errs
}

func byNumber(t *testing.T, specs []*feature.Spec, number int) *feature.Spec {
	t.Helper()
	for _, spec := range specs {
		if issueNumber(spec) == number {
			return spec
		}
	}
	t.Fatalf("nenhum card para a issue #%d (li %d cards)", number, len(specs))
	return nil
}

// The whole point of the source: an issue lands in the column fila decided,
// not in one this package invented. #160 is assigned AND hitl, and hitl wins.
func TestColumnIsFilasDecisionIncludingHITLOverAssignee(t *testing.T) {
	run := healthy()
	specs, errs := load(t, run, kitPath, slug)
	if len(errs) != 0 {
		t.Fatalf("fila saudavel nao devia reportar problema: %v", errs)
	}
	if len(specs) != 3 {
		t.Fatalf("li %d cards, quero 3", len(specs))
	}

	for _, want := range []struct {
		number int
		status feature.Status
		why    string
	}{
		{160, feature.Blocked, "hitl assumida continua bloqueio"},
		{31, feature.Backlog, "task livre e nao rankeada como bloqueio"},
		{12, feature.Done, "fechada e fechada, antes de qualquer label"},
	} {
		if got := byNumber(t, specs, want.number).Status; got != want.status {
			t.Errorf("#%d caiu em %q, quero %q: %s", want.number, got, want.status, want.why)
		}
	}
}

// A frontier ranking is the sort order and the rank label, and a blocked
// number moves the column.
func TestFrontierRanksAndBlocks(t *testing.T) {
	run := healthy()
	specs, _ := load(t, run, kitPath, slug)
	if got := issueNumber(specs[0]); got != 31 {
		t.Errorf("primeiro card e #%d, quero #31: a fronteira rankeou essa", got)
	}
	if !specs[0].HasLabel(LabelRankPrefix + "0") {
		t.Errorf("card rankeado nao carrega %q: %v", LabelRankPrefix+"0", specs[0].Labels)
	}
	if !strings.Contains(specs[0].Body, "alinhamento: receita") {
		t.Errorf("o porque da fronteira nao chegou ao card: %q", specs[0].Body)
	}
	if byNumber(t, specs, 160).HasLabel(LabelRankPrefix + "0") {
		t.Error("card nao rankeado ganhou rank, o que inventa posicao")
	}

	blocked := newFake(map[string]answer{
		"usina rota show":  {stdout: routeBody},
		"usina issue list": {stdout: listBody},
		"python3 " + kitPath: {stdout: `{"pronto": [], "bloqueado": ` +
			`[{"titulo": "CI vermelho no deploy", "numero": 31, "porque": "espera credencial"}]}`},
	})
	specs, _ = load(t, blocked, kitPath, slug)
	if got := byNumber(t, specs, 31).Status; got != feature.Blocked {
		t.Errorf("#31 na lista de bloqueado caiu em %q, quero %q", got, feature.Blocked)
	}
}

// Without kitPath and slug the board is narrower, never broken, and kit.py is
// not executed at all.
func TestWithoutFrontierTheBoardStillDrawsAndKitIsNotRun(t *testing.T) {
	run := healthy()
	specs, errs := load(t, run, "", "")
	if len(errs) != 0 {
		t.Fatalf("sem fronteira pedida nao ha problema a reportar: %v", errs)
	}
	if len(specs) != 3 {
		t.Fatalf("li %d cards, quero 3", len(specs))
	}
	if run.asked("python3") {
		t.Error("kit.py foi executado sem kitPath/slug")
	}
	for _, spec := range specs {
		for _, label := range spec.Labels {
			if strings.HasPrefix(label, LabelRankPrefix) {
				t.Errorf("card ganhou %q sem fronteira medida", label)
			}
		}
	}
}

// A degradation is a reported problem, not silence, and it is visible on the
// card itself. This is the property the whole composition exists for.
func TestDegradationToGHIsReportedAndLabelled(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show":    {stdout: routeBody},
		"usina issue list":   {err: errors.New("exit status 1")},
		"gh issue list":      {stdout: listBody},
		"python3 " + kitPath: {stdout: frontierBody},
	})
	specs, errs := load(t, run, kitPath, slug)
	if len(specs) != 3 {
		t.Fatalf("degradacao perdeu cards: li %d, quero 3", len(specs))
	}
	if len(errs) == 0 {
		t.Fatal("degradacao silenciosa: nenhum problema reportado")
	}
	joined := joinErrors(errs)
	if !strings.Contains(joined, "gh") {
		t.Errorf("o problema nao nomeia quem respondeu: %q", joined)
	}
	for _, spec := range specs {
		if !spec.HasLabel(LabelOriginGH) {
			t.Errorf("card lido pelo gh nao carrega %q: %v", LabelOriginGH, spec.Labels)
		}
		if spec.HasLabel(LabelOriginUsina) {
			t.Errorf("card lido pelo gh carrega %q", LabelOriginUsina)
		}
	}
}

// An unread frontier costs the ranking and says so; it never costs the cards.
func TestUnreadFrontierKeepsTheCardsAndNamesTheFailure(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show":    {stdout: routeBody},
		"usina issue list":   {stdout: listBody},
		"python3 " + kitPath: {stdout: "nao e json"},
	})
	specs, errs := load(t, run, kitPath, slug)
	if len(specs) != 3 {
		t.Fatalf("fronteira ilegivel derrubou cards: li %d, quero 3", len(specs))
	}
	if !strings.Contains(joinErrors(errs), "unranked") {
		t.Errorf("fronteira ilegivel nao foi reportada como perda de ranking: %q", joinErrors(errs))
	}
}

// The measured collision, pinned. kit.py ranks aryrabelo/bugtoprompt#31 ("CI
// red: deploy on main"); aryrabelo/ceo-bora#31 is an unrelated issue. Joining
// the frontier onto this queue by number alone would have moved a card that
// merely shares an integer, so the join is refused and said out loud.
func TestFrontierOfAnotherRepositoryIsNeverJoinedByNumber(t *testing.T) {
	blocking := `{"pronto": [], "bloqueado": [{"titulo": "CI red: deploy on main", ` +
		`"numero": 160, "porque": "espera credencial"}]}`
	run := newFake(map[string]answer{
		"usina rota show":    {stdout: routeBody},
		"usina issue list":   {stdout: listBody},
		"python3 " + kitPath: {stdout: blocking},
	})

	specs, errs := New(run.run, ceoRepo, label, kitPath, slug,
		"aryrabelo/bugtoprompt", 30).Load(context.Background())
	if len(specs) != 3 {
		t.Fatalf("li %d cards, quero 3", len(specs))
	}
	joined := joinErrors(errs)
	if !strings.Contains(joined, "different issues") {
		t.Errorf("a recusa de juntar nao foi dita: %q", joined)
	}
	if !strings.Contains(joined, "aryrabelo/bugtoprompt") || !strings.Contains(joined, ceoRepo) {
		t.Errorf("a recusa nao nomeia os dois repos: %q", joined)
	}
	for _, spec := range specs {
		for _, label := range spec.Labels {
			if strings.HasPrefix(label, LabelRankPrefix) {
				t.Errorf("%s ganhou %q de uma fronteira de outro repo", spec.ID, label)
			}
		}
	}
	// #160 is hitl and therefore Blocked anyway; the point is that it is
	// blocked for its own reason, and that a frontier from elsewhere cannot
	// move a card that is not.
	if got := byNumber(t, specs, 31).Status; got != feature.Backlog {
		t.Errorf("#31 caiu em %q por colisao de numero, quero %q", got, feature.Backlog)
	}

	// The same frontier, asked for without naming any repository at all, is
	// equally refused: silence is not permission.
	_, errs = New(run.run, ceoRepo, label, kitPath, slug, "", 30).Load(context.Background())
	if !strings.Contains(joinErrors(errs), "nothing named the repository") {
		t.Errorf("fronteira sem repo nomeado foi aceita em silencio: %q", joinErrors(errs))
	}
}

// The route's refusal is data: the cards it did route are still read.
//
// Measured shape: `recusa` lives INSIDE the area that refused, never at the
// top level. `usina rota show --repo aryrabelo/ceo-bora` really answers
// `"kb": {"recusa": "'kb' ausente ou nao-mapping…"}` while routing `issues`
// perfectly well, which is exactly why the refusal must not be fatal.
func TestRouteRefusalIsReportedWithoutLosingCards(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show": {stdout: `{"repo": "aryrabelo/ceo-bora", ` +
			`"issues": "aryrabelo/ceo-bora", "config": "ceo-bora.yml", ` +
			`"kb": {"recusa": "'kb' ausente ou nao-mapping em usina-config (ceo-bora.yml)"}}`},
		"usina issue list":   {stdout: listBody},
		"python3 " + kitPath: {stdout: frontierBody},
	})
	specs, errs := load(t, run, kitPath, slug)
	if len(specs) != 3 {
		t.Fatalf("recusa da rota derrubou cards: li %d, quero 3", len(specs))
	}
	if !strings.Contains(joinErrors(errs), "'kb' ausente") {
		t.Errorf("a recusa da rota nao chegou ao usuario: %q", joinErrors(errs))
	}
}

// Both binaries dead is a hard failure, and it names them instead of drawing
// an empty board that looks like an empty queue.
func TestBothSourcesDeadIsAnErrorNotAnEmptyBoard(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show":  {err: errors.New("no such file")},
		"usina issue list": {err: errors.New("no such file")},
		"gh issue list":    {err: errors.New("no such file")},
	})
	specs, errs := load(t, run, kitPath, slug)
	if len(specs) != 0 {
		t.Errorf("li %d cards sem fonte viva", len(specs))
	}
	if len(errs) == 0 {
		t.Fatal("fila sem fonte viva passou como fila vazia")
	}
	joined := joinErrors(errs)
	if !strings.Contains(joined, "usina") || !strings.Contains(joined, "gh") {
		t.Errorf("o erro nao nomeia as duas fontes: %q", joined)
	}
}

// A repository label spelling the reserved prefix is content anyone can edit
// in the GitHub UI, so it must never reach the card.
func TestReservedPrefixCannotBeForgedByARepositoryLabel(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show": {stdout: routeBody},
		"usina issue list": {stdout: `[{"number": 7, "title": "forja", ` +
			`"url": "u", "state": "OPEN", "labels": [{"name": "hvb:rank:0"}, ` +
			`{"name": "hvb:source:pr"}, {"name": "real"}], "assignees": []}]`},
	})
	specs, _ := load(t, run, "", "")
	if len(specs) != 1 {
		t.Fatalf("li %d cards, quero 1", len(specs))
	}
	if specs[0].HasLabel("hvb:source:pr") {
		t.Error("label forjada hvb:source:pr chegou ao card e poderia mover a coluna cancelada")
	}
	if specs[0].HasLabel(LabelRankPrefix + "0") {
		t.Error("label forjada de rank chegou ao card sem fronteira medida")
	}
	if !specs[0].HasLabel("real") {
		t.Errorf("label real do repositorio foi descartada: %v", specs[0].Labels)
	}
}

// fila and the vb lifecycle spell the same five lanes. If either renames one,
// a card would carry a status the board cannot draw, and the column would
// silently vanish from the board rather than error.
func TestFilaColumnsAndFeatureStatusesAgree(t *testing.T) {
	pairs := []struct {
		column fila.Column
		status feature.Status
	}{
		{fila.Backlog, feature.Backlog},
		{fila.InProgress, feature.InProgress},
		{fila.Blocked, feature.Blocked},
		{fila.Review, feature.Review},
		{fila.Done, feature.Done},
	}
	if len(pairs) != len(feature.VB().Columns()) {
		t.Fatalf("o workflow do vb tem %d colunas e este teste conhece %d", len(feature.VB().Columns()), len(pairs))
	}
	for _, pair := range pairs {
		if string(pair.column) != string(pair.status) {
			t.Errorf("fila diz %q e feature diz %q", pair.column, pair.status)
		}
		if !feature.VB().Has(feature.Status(string(pair.column))) {
			t.Errorf("%q nao e um status que o board desenha", pair.column)
		}
	}
}

// The line policy reads the state off the label, not off the column: the
// column is fila's derivation and a `[workflow]` declaration can rename it,
// while "the issue is closed" is a fact GitHub reported. Exactly one of the
// two is on every card — two would put a card in two columns at once, and
// none would make the policy fall back on a column it cannot trust.
func TestEveryCardCarriesTheMeasuredIssueState(t *testing.T) {
	specs, errs := load(t, healthy(), kitPath, slug)
	if len(errs) != 0 {
		t.Fatalf("fila saudavel nao devia reportar problema: %v", errs)
	}

	want := map[int]string{
		// #12 is the only closed one in the measured queue.
		12: LabelStateClosed,
		// #160 is open AND blocked by hitl, so its column is Blocked
		// rather than Backlog: the state label is the state, not the
		// column.
		160: LabelStateOpen,
		31:  LabelStateOpen,
	}
	if len(want) != len(specs) {
		t.Fatalf("li %d cards e este teste conhece %d", len(specs), len(want))
	}

	for number, expected := range want {
		spec := byNumber(t, specs, number)
		var got []string
		for _, label := range spec.Labels {
			if strings.HasPrefix(label, LabelPrefix+"state:") {
				got = append(got, label)
			}
		}
		if len(got) != 1 {
			t.Errorf("#%d carrega %v, quero exatamente uma label de estado", number, got)
			continue
		}
		if got[0] != expected {
			t.Errorf("#%d estado = %q, quero %q", number, got[0], expected)
		}
	}

	// The column stays fila's answer: minting the fact must not move a
	// card. #12 closed is Done and #160 blocked is Blocked, as before.
	if got := byNumber(t, specs, 12).Status; got != feature.Done {
		t.Errorf("#12 caiu em %q, quero %q: a coluna continua sendo decisao da fila", got, feature.Done)
	}
	if got := byNumber(t, specs, 160).Status; got != feature.Blocked {
		t.Errorf("#160 caiu em %q, quero %q: a coluna continua sendo decisao da fila", got, feature.Blocked)
	}
}

// The id names the repository that owns the issue, so two queues read side by
// side cannot collide on a number.
func TestIDNamesTheOwningRepository(t *testing.T) {
	if got := ID("aryrabelo/ceo-bora", 160); got != "ceo-bora#160" {
		t.Errorf("ID = %q, quero \"ceo-bora#160\"", got)
	}
	if got := ID("", 160); got != "issue#160" {
		t.Errorf("ID sem repo = %q, quero \"issue#160\"", got)
	}
	if ID("aryrabelo/ceo-bora", 31) == ID("aryrabelo/outro", 31) {
		t.Error("duas filas colidem no mesmo numero")
	}
}

// A mis-wired call fails where there is nothing to degrade from.
func TestNoRunnerIsAWiringErrorNotAnEmptyQueue(t *testing.T) {
	_, errs := New(nil, ceoRepo, label, "", "", "", 0).Load(context.Background())
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "no runner") {
		t.Fatalf("errs = %v, quero um erro nomeando a falta do runner", errs)
	}
}

// A cancelled context is reported, not worked around with a stale read.
func TestCancelledContextIsReported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := healthy()
	_, errs := New(run.run, ceoRepo, label, kitPath, slug, ceoRepo, 30).Load(ctx)
	if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("errs = %v, quero context.Canceled", errs)
	}
	if len(run.calls) != 0 {
		t.Errorf("contexto cancelado ainda executou %v", run.calls)
	}
}

func joinErrors(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, " | ")
}

// DirRunner exists because usina and kit.py read their identity off the
// working directory, so a runner that ignores dir turns the whole board into
// two declared degradations. This is the one test here that really executes a
// binary — /bin/pwd, which is the only thing that can prove the child landed
// where it was told.
func TestDirRunnerRunsChildrenInTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	stdout, err := DirRunner(dir)("pwd")
	if err != nil {
		t.Fatalf("pwd em %s: %v", dir, err)
	}
	// pwd reports the logical path, which on macOS is the symlinked
	// /var/folders form of the same directory; comparing the resolved form
	// is what makes this test pass for the right reason on both platforms.
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(stdout)))
	if err != nil {
		t.Fatalf("EvalSymlinks do resultado: %v", err)
	}
	if got != resolved {
		t.Errorf("filho rodou em %q, quero %q: usina resolveria a instancia errada", got, resolved)
	}

	// An empty dir must not mean "some other directory": it means here.
	stdout, err = DirRunner("  ")("pwd")
	if err != nil {
		t.Fatalf("pwd sem dir: %v", err)
	}
	here, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if strings.TrimSpace(string(stdout)) != here {
		t.Errorf("dir vazio rodou em %q, quero o diretorio atual %q",
			strings.TrimSpace(string(stdout)), here)
	}
}
