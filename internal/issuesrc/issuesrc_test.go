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
	slug    = "bugtoprompt"
	// frontierOwner is the repository whose issues kit.py really ranks for
	// the bugtoprompt project: measured 2026-09-17,
	// projetos/bugtoprompt/charter.md says `repo: aryrabelo/bugtoprompt`.
	frontierOwner = "aryrabelo/bugtoprompt"
)

// charterOf is where kit.py reads a project's repository from, spelled here
// independently of the production helper on purpose: if the layout drifts, the
// fixtures stop being found and these tests fail, instead of following the
// change wherever it went.
func charterOf(kit, project string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(kit)), "projetos", project, "charter.md")
}

// writeCharter writes one project charter with the given front matter and
// returns its path.
func writeCharter(t *testing.T, kit, project, frontMatter string) string {
	t.Helper()
	path := charterOf(kit, project)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir do charter: %v", err)
	}
	body := "---\n" + frontMatter + "\n---\n\n# charter\n\nO repo: desta prosa nao e front matter.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("escrever o charter: %v", err)
	}
	return path
}

// kitIn builds a throwaway CEO repository — kit.py at bin/kit.py, plus the
// project charter naming repo when it is not empty — and returns the kit.py
// path. Nothing here is ever executed: the script exists only because the
// repository root is derived from its path.
func kitIn(t *testing.T, repo string) string {
	t.Helper()
	kit := filepath.Join(t.TempDir(), "bin", "kit.py")
	if err := os.MkdirAll(filepath.Dir(kit), 0o755); err != nil {
		t.Fatalf("mkdir do bin: %v", err)
	}
	if err := os.WriteFile(kit, []byte("# nunca executado\n"), 0o644); err != nil {
		t.Fatalf("escrever kit.py: %v", err)
	}
	if repo != "" {
		writeCharter(t, kit, slug, "repo: "+repo)
	}
	return kit
}

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
		"usina rota show":  {stdout: routeBody},
		"usina issue list": {stdout: listBody},
		"python3":          {stdout: frontierBody},
	})
}

// load reads the queue with the frontier repository derived from the charter,
// which is how the command asks for it: nothing declares it.
func load(t *testing.T, run *fake, kit, project string) ([]*feature.Spec, []error) {
	t.Helper()
	specs, errs := New(run.run, ceoRepo, label, kit, project, "", 30).Load(context.Background())
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
	specs, errs := load(t, run, kitIn(t, ceoRepo), slug)
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
	kit := kitIn(t, ceoRepo)
	specs, _ := load(t, run, kit, slug)
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
		"python3": {stdout: `{"pronto": [], "bloqueado": ` +
			`[{"titulo": "CI vermelho no deploy", "numero": 31, "porque": "espera credencial"}]}`},
	})
	specs, _ = load(t, blocked, kit, slug)
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
		"usina rota show":  {stdout: routeBody},
		"usina issue list": {err: errors.New("exit status 1")},
		"gh issue list":    {stdout: listBody},
		"python3":          {stdout: frontierBody},
	})
	specs, errs := load(t, run, kitIn(t, ceoRepo), slug)
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
		"usina rota show":  {stdout: routeBody},
		"usina issue list": {stdout: listBody},
		"python3":          {stdout: "nao e json"},
	})
	specs, errs := load(t, run, kitIn(t, ceoRepo), slug)
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
//
// This is today's real wiring: the board reads aryrabelo/ceo-bora's issues and
// projetos/bugtoprompt/charter.md says kit.py ranks aryrabelo/bugtoprompt.
func TestFrontierOfAnotherRepositoryIsNeverJoinedByNumber(t *testing.T) {
	blocking := `{"pronto": [], "bloqueado": [{"titulo": "CI red: deploy on main", ` +
		`"numero": 160, "porque": "espera credencial"}]}`
	run := newFake(map[string]answer{
		"usina rota show":  {stdout: routeBody},
		"usina issue list": {stdout: listBody},
		"python3":          {stdout: blocking},
	})

	specs, errs := load(t, run, kitIn(t, frontierOwner), slug)
	if len(specs) != 3 {
		t.Fatalf("li %d cards, quero 3", len(specs))
	}
	joined := joinErrors(errs)
	if !strings.Contains(joined, "different issues") {
		t.Errorf("a recusa de juntar nao foi dita: %q", joined)
	}
	if !strings.Contains(joined, frontierOwner) || !strings.Contains(joined, ceoRepo) {
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
}

// The repository kit.py ranks is derived, not declared: the charter is the
// same file kit.py reads to decide it, so a board pointed at that repository
// ranks without anybody typing an owner/name.
func TestFrontierRepositoryIsDerivedFromTheCharter(t *testing.T) {
	// The queue itself is aryrabelo/ceo-bora, so this is the charter of a
	// project whose work lives in the CEO repository.
	kit := kitIn(t, ceoRepo)
	specs, errs := load(t, healthy(), kit, slug)
	if len(errs) != 0 {
		t.Fatalf("repo derivado do charter nao devia reportar problema: %v", errs)
	}
	if !specs[0].HasLabel(LabelRankPrefix + "0") {
		t.Errorf("a fronteira nao foi juntada com o repo derivado: %v", specs[0].Labels)
	}
	if got := charterOf(kit, slug); !strings.Contains(got, filepath.Join("projetos", slug)) {
		t.Fatalf("o charter lido foi %q, que nao e o caminho que kit.py le", got)
	}
}

// A declared --kit-repo the charter contradicts is a wrong declaration, and it
// has to read as one: the alternative is a board that ranks by number because
// a human typed the wrong owner/name.
func TestDeclaredKitRepoTheCharterContradictsRefusesTheJoin(t *testing.T) {
	kit := kitIn(t, ceoRepo)
	charter := charterOf(kit, slug)
	specs, errs := New(healthy().run, ceoRepo, label, kit, slug, frontierOwner, 30).
		Load(context.Background())
	if len(specs) != 3 {
		t.Fatalf("li %d cards, quero 3", len(specs))
	}
	joined := joinErrors(errs)
	for _, want := range []string{frontierOwner, ceoRepo, charter} {
		if !strings.Contains(joined, want) {
			t.Errorf("a recusa nao nomeia %q: %q", want, joined)
		}
	}
	if specs[0].HasLabel(LabelRankPrefix + "0") {
		t.Errorf("a fronteira foi juntada com a declaracao desmentida: %v", specs[0].Labels)
	}
}

// An agreeing declaration is not an error: it says the same thing the charter
// says, and case is not a disagreement.
func TestDeclaredKitRepoAgreeingWithTheCharterJoins(t *testing.T) {
	kit := kitIn(t, ceoRepo)
	specs, errs := New(healthy().run, ceoRepo, label, kit, slug,
		strings.ToUpper(ceoRepo), 30).Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("declaracao concordante foi tratada como erro: %v", errs)
	}
	if !specs[0].HasLabel(LabelRankPrefix + "0") {
		t.Errorf("declaracao concordante impediu a juncao: %v", specs[0].Labels)
	}
}

// Absence is absence. Without a charter there is nothing that names the
// ranked repository — kit.py reads that same file, so it could not have ranked
// this queue either — and the refusal names the exact path that was looked up
// instead of falling back to the CEO repository.
func TestMissingCharterRefusesTheJoinAndNamesThePath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		front string
	}{
		{name: "sem charter"},
		{name: "charter sem repo", front: "slug: bugtoprompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kit := kitIn(t, "")
			if tc.front != "" {
				writeCharter(t, kit, slug, tc.front)
			}
			specs, errs := load(t, healthy(), kit, slug)
			if len(specs) != 3 {
				t.Fatalf("li %d cards, quero 3", len(specs))
			}
			joined := joinErrors(errs)
			if !strings.Contains(joined, charterOf(kit, slug)) {
				t.Errorf("a recusa nao nomeia o caminho procurado %q: %q",
					charterOf(kit, slug), joined)
			}
			if !strings.Contains(joined, "does not name the repository") {
				t.Errorf("a recusa nao diz que nada nomeou o repo: %q", joined)
			}
			for _, spec := range specs {
				for _, label := range spec.Labels {
					if strings.HasPrefix(label, LabelRankPrefix) {
						t.Errorf("%s ganhou %q sem charter que nomeie o repo",
							spec.ID, label)
					}
				}
			}
		})
	}
}

// A sibling area's refusal is not this board's problem, and the `issues`
// area's own refusal is.
//
// Measured shape: `recusa` lives INSIDE the area that refused, never at the
// top level. `usina rota show --repo aryrabelo/ceo-bora` really answers
// `"kb": {"recusa": "'kb' ausente ou nao-mapping…"}` while routing `issues`
// perfectly well — kb is the team's knowledge base, which no kanban card is
// ever read from — so surfacing it lit a problem on every single board over
// this repository and taught the owner to ignore the problem line.
func TestSiblingAreaRefusalIsNotAProblemButTheIssuesAreaIs(t *testing.T) {
	run := newFake(map[string]answer{
		"usina rota show": {stdout: `{"repo": "aryrabelo/ceo-bora", ` +
			`"issues": "aryrabelo/ceo-bora", "config": "ceo-bora.yml", ` +
			`"kb": {"recusa": "'kb' ausente ou nao-mapping em usina-config (ceo-bora.yml)"}}`},
		"usina issue list": {stdout: listBody},
		"python3":          {stdout: frontierBody},
	})
	specs, errs := load(t, run, kitIn(t, ceoRepo), slug)
	if len(specs) != 3 {
		t.Fatalf("recusa de area vizinha derrubou cards: li %d, quero 3", len(specs))
	}
	if len(errs) != 0 {
		t.Errorf("recusa do 'kb' virou problema num board que nunca le kb: %v", errs)
	}

	// The area this board does read is the opposite case: its refusal is
	// about the queue itself, so it has to be said.
	refused := newFake(map[string]answer{
		"usina rota show": {stdout: `{"repo": "aryrabelo/ceo-bora", ` +
			`"issues": {"recusa": "'issues' ausente em usina-config (ceo-bora.yml)"}}`},
		"usina issue list": {stdout: listBody},
		"python3":          {stdout: frontierBody},
	})
	specs, errs = load(t, refused, kitIn(t, ceoRepo), slug)
	if len(specs) != 3 {
		t.Fatalf("recusa da area issues derrubou cards: li %d, quero 3", len(specs))
	}
	if !strings.Contains(joinErrors(errs), "'issues' ausente") {
		t.Errorf("a recusa da propria area issues nao chegou ao usuario: %q", joinErrors(errs))
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
	specs, errs := load(t, run, kitIn(t, ceoRepo), slug)
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
	specs, errs := load(t, healthy(), kitIn(t, ceoRepo), slug)
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
	_, errs := New(run.run, ceoRepo, label, kitIn(t, ceoRepo), slug, "", 30).Load(ctx)
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

// ceoDirIn builds a real directory whose last segment is name, so a path that
// usina would accept can be handed to a runner that really runs something.
func ceoDirIn(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir de %s: %v", dir, err)
	}
	return dir
}

// pwdOf is the directory a runner's children really land in, which is the only
// thing that proves WHICH directory reached the runner: usina reads its
// instance off exactly this value.
func pwdOf(t *testing.T, run Runner) string {
	t.Helper()
	stdout, err := run("pwd")
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	return resolved(t, strings.TrimSpace(string(stdout)))
}

// resolved is dir with the symlinks gone: on macOS a t.TempDir() lives under
// /var, which pwd reports as /private/var, and comparing the two raw forms
// would fail for a reason that has nothing to do with the code.
func resolved(t *testing.T, dir string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks de %s: %v", dir, err)
	}
	return out
}

// The two facts that used to travel in one flag. --kit means "rank the cards";
// where the CEO repository is, is a separate question, and the answer to it
// wins. Measured 2026-09-17: a board opened from an execution repository
// without --kit ran usina from there, got "instância da usina indeterminada:
// nenhum segmento 'ceo-<nome>' no cwd", and read the whole queue through gh.
func TestDeclaredCEORootBeatsTheKitDerivation(t *testing.T) {
	t.Setenv("USINA_CONFIG", "")
	declared := ceoDirIn(t, "ceo-bora")
	kit := kitIn(t, frontierOwner)
	if CEORoot(kit) == declared {
		t.Fatalf("fixture inutil: --kit deriva o mesmo diretorio %q", declared)
	}

	dir, problem := WorkDir(declared, kit)
	if dir != declared {
		t.Fatalf("WorkDir = %q, quero o declarado %q", dir, declared)
	}
	if problem != nil {
		t.Errorf("%q tem segmento ceo-, nao ha o que reportar: %v", declared, problem)
	}
	if got := pwdOf(t, DirRunner(dir)); got != resolved(t, declared) {
		t.Errorf("filho rodou em %q, quero %q: a usina resolveria a instancia errada", got, resolved(t, declared))
	}
}

// The derivation that exists today stays alive: a caller passing only the
// frontier flags keeps the directory it has always got, so nothing that works
// now needs a second flag to keep working.
func TestWithoutADeclaredCEORootTheKitDerivationSurvives(t *testing.T) {
	kit := kitIn(t, frontierOwner)
	dir, _ := WorkDir("   ", kit)
	if dir != CEORoot(kit) {
		t.Fatalf("WorkDir = %q, quero a derivacao de --kit %q", dir, CEORoot(kit))
	}
	if got := pwdOf(t, DirRunner(dir)); got != resolved(t, CEORoot(kit)) {
		t.Errorf("filho rodou em %q, quero %q", got, resolved(t, CEORoot(kit)))
	}
}

// With neither answer there is nothing to derive from, and a path invented
// here would send every child somewhere nobody asked for. Empty means here,
// which is right for the board launched from inside the CEO checkout.
func TestWithNeitherAnswerTheChildrenRunWhereTheBoardWasLaunched(t *testing.T) {
	dir, _ := WorkDir("", "")
	if dir != "" {
		t.Fatalf("WorkDir inventou %q; sem os dois so existe o diretorio atual", dir)
	}
	here, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if got := pwdOf(t, DirRunner(dir)); got != resolved(t, here) {
		t.Errorf("filho rodou em %q, quero o diretorio atual %q", got, resolved(t, here))
	}
}

// usina's refusal is a property of the PATH, so the board can measure it
// before spending a subprocess on it. It is reported and the board still
// opens: the gh fallback answers, so refusing to open would cost the user a
// board that works in part.
func TestADirectoryWithoutACEOSegmentIsReportedOnTheBoard(t *testing.T) {
	t.Setenv("USINA_CONFIG", "")
	declared := ceoDirIn(t, "bugtoprompt")

	_, problem := WorkDir(declared, "")
	if problem == nil {
		t.Fatalf("%q nao tem segmento ceo-: a usina recusa e o board tem de dizer isso", declared)
	}
	if !strings.Contains(problem.Error(), declared) {
		t.Errorf("o problema nao nomeia o diretorio %q: %v", declared, problem)
	}
	if !strings.Contains(problem.Error(), "ceo-") {
		t.Errorf("o problema nao diz o que falta no caminho: %v", problem)
	}

	run := healthy()
	specs, errs := New(run.run, ceoRepo, label, "", "", "", 30).
		Note(problem).
		Load(context.Background())
	if len(specs) != 3 {
		t.Fatalf("li %d cards, quero 3: a recusa da usina nao pode fechar o board", len(specs))
	}
	if joined := joinErrors(errs); !strings.Contains(joined, declared) {
		t.Errorf("o board nao disse em que diretorio os filhos rodam: %q", joined)
	}
}

// A directory usina accepts has nothing to report, including a worktree, whose
// name keeps the ceo-<name> prefix. A board that complained here would be a
// board whose problem line the owner learns to ignore.
func TestADirectoryWithACEOSegmentHasNothingToSay(t *testing.T) {
	t.Setenv("USINA_CONFIG", "")
	for _, name := range []string{"ceo-bora", "ceo-pp", "ceo-bora-epic-269"} {
		if _, problem := WorkDir(ceoDirIn(t, name), ""); problem != nil {
			t.Errorf("%s e uma instancia da usina, nada a reportar: %v", name, problem)
		}
	}
}

// "ceo-" inside a word is not a segment: usina resolves the instance from a
// path SEGMENT named ceo-<name>, so staying quiet for traceo-tmp would be
// staying quiet about a board that is about to be read by gh.
func TestCEOInsideAWordIsNotAnInstance(t *testing.T) {
	t.Setenv("USINA_CONFIG", "")
	for _, name := range []string{"traceo-tmp", "ceo", "ceo-"} {
		if _, problem := WorkDir(ceoDirIn(t, name), ""); problem == nil {
			t.Errorf("%s nao nomeia instancia nenhuma, a usina recusaria em silencio", name)
		}
	}
}

// USINA_CONFIG names the instance outright — it is the second half of usina's
// own refusal ("ou aponte USINA_CONFIG pro yml da instância") — so with it set
// the path is no longer evidence of anything.
func TestUsinaConfigMakesThePathIrrelevant(t *testing.T) {
	t.Setenv("USINA_CONFIG", filepath.Join(t.TempDir(), "instancia.yml"))
	if _, problem := WorkDir(ceoDirIn(t, "bugtoprompt"), ""); problem != nil {
		t.Errorf("com USINA_CONFIG a usina responde de qualquer diretorio: %v", problem)
	}
}
