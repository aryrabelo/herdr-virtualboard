package fila

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeRunner answers one kit.py call with canned bytes and records how it was
// invoked. No test in this package executes python3, kit.py, gh, or usina.
func fakeRunner(t *testing.T, stdout string, err error, seen *[]string) Runner {
	t.Helper()
	return func(name string, args ...string) ([]byte, error) {
		if seen != nil {
			*seen = append([]string{name}, args...)
		}
		return []byte(stdout), err
	}
}

// kitAnswer is the exact shape measured from `kit.py fronteira <slug>`:
// `bloqueado` is an array of objects with the same keys as `pronto`, not an
// array of integers. Decoding it as integers is the mistake this fixture
// exists to prevent.
const kitAnswer = `{
 "verbo": "fronteira",
 "slug": "bugtoprompt",
 "pronto": [
  {"titulo": "CI red: deploy on main", "numero": 31, "rank": 0, "porque": "alinhamento: receita"},
  {"titulo": "cortar o Dolt central", "numero": 44, "rank": 2, "porque": "desbloqueia 3"}
 ],
 "bloqueado": [
  {"titulo": "mintar credencial", "numero": 12, "porque": "bloqueador aberto"},
  {"titulo": "epico do board", "numero": 7, "porque": "tem 2 filho(s) aberto(s)"}
 ]
}`

func TestBlockedComesBackAsObjectsNotIntegers(t *testing.T) {
	var seen []string
	frontier, err := LoadFrontier(fakeRunner(t, kitAnswer, nil, &seen), "bin/kit.py", "bugtoprompt")
	if err != nil {
		t.Fatalf("LoadFrontier devolveu erro numa leitura boa: %v", err)
	}
	if frontier.Reason != "" {
		t.Fatalf("fronteira medida veio com Reason %q; motivo so existe quando degrada", frontier.Reason)
	}
	if got := strings.Join(seen, " "); got != "python3 bin/kit.py fronteira bugtoprompt" {
		t.Fatalf("kit.py chamado como %q; esperado \"python3 bin/kit.py fronteira bugtoprompt\"", got)
	}
	for _, numero := range []int{12, 7} {
		if !frontier.Blocked[numero] {
			t.Fatalf("issue %d nao entrou no bloqueado; `bloqueado` e array de OBJETOS, com numero dentro", numero)
		}
	}
	if len(frontier.Blocked) != 2 {
		t.Fatalf("bloqueado tem %d entradas; esperado 2", len(frontier.Blocked))
	}
	if frontier.Blocked[31] {
		t.Fatalf("issue 31 esta em `pronto` e apareceu como bloqueada")
	}
}

// TestRankZeroIsAMeasuredRankNotAbsence: kit.py ranks the top of the frontier
// and rank 0 is a real position. Has is the only absence signal; reading rank
// as "unset" would silently demote the single most important card.
func TestRankZeroIsAMeasuredRankNotAbsence(t *testing.T) {
	frontier, err := LoadFrontier(fakeRunner(t, kitAnswer, nil, nil), "bin/kit.py", "bugtoprompt")
	if err != nil {
		t.Fatalf("LoadFrontier devolveu erro numa leitura boa: %v", err)
	}

	top, ok := frontier.Priority[31]
	if !ok {
		t.Fatalf("issue 31 sumiu do mapa; rank 0 e rank medido, nao ausencia")
	}
	if !top.Has {
		t.Fatalf("issue 31 voltou com Has=false; rank 0 e rank medido, nao ausencia")
	}
	if top.Rank != 0 {
		t.Fatalf("issue 31 voltou com rank %d; kit.py disse 0", top.Rank)
	}
	if top.Why != "alinhamento: receita" {
		t.Fatalf("issue 31 voltou com porque %q; kit.py disse \"alinhamento: receita\"", top.Why)
	}

	if second := frontier.Priority[44]; !second.Has || second.Rank != 2 {
		t.Fatalf("issue 44 voltou %+v; esperado rank 2 com Has=true", second)
	}
}

// TestIssuesTheFrontierNeverMentionedStayOutOfTheMap: absence is absence. An
// unmentioned issue must not materialise as a rank-zero card.
func TestIssuesTheFrontierNeverMentionedStayOutOfTheMap(t *testing.T) {
	frontier, err := LoadFrontier(fakeRunner(t, kitAnswer, nil, nil), "bin/kit.py", "bugtoprompt")
	if err != nil {
		t.Fatalf("LoadFrontier devolveu erro numa leitura boa: %v", err)
	}
	missing, ok := frontier.Priority[999]
	if ok {
		t.Fatalf("issue 999 nunca foi citada e veio no mapa como %+v", missing)
	}
	if missing.Has {
		t.Fatalf("zero value de Priority veio com Has=true; Has e o unico sinal de presenca")
	}
}

// TestEmptyFrontierStillReturnsIndexableMapsAndAReason is the degradation
// contract: the board repaints without a frontier, so the maps must be safe to
// index and write to, and the silence must carry its own explanation.
func TestEmptyFrontierStillReturnsIndexableMapsAndAReason(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		err    error
		want   string
	}{
		{
			name:   "kit ausente ou exit != 0",
			stdout: "",
			err:    errors.New("python3 saiu 1: fronteira: nao consegui ler as issues"),
			want:   "nao respondeu",
		},
		{
			name:   "stdout nao e JSON",
			stdout: "Traceback (most recent call last):",
			err:    nil,
			want:   "nao e JSON",
		},
		{
			name:   "pronto e bloqueado vazios",
			stdout: `{"verbo":"fronteira","slug":"bugtoprompt","pronto":[],"bloqueado":[]}`,
			err:    nil,
			want:   "sem pronto nem bloqueado",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frontier, err := LoadFrontier(fakeRunner(t, tc.stdout, tc.err, nil), "bin/kit.py", "bugtoprompt")
			if err != nil {
				t.Fatalf("fonte muda virou erro (%v); o board tem de renderizar sem fronteira", err)
			}
			if frontier.Priority == nil || frontier.Blocked == nil {
				t.Fatalf("fronteira vazia voltou com mapa nil (Priority=%v Blocked=%v); o board indexa e escreve neles",
					frontier.Priority, frontier.Blocked)
			}
			// A nil map panics on write, not on read: exercise the write.
			frontier.Priority[1] = Priority{Has: true}
			frontier.Blocked[1] = true

			if !strings.Contains(frontier.Reason, tc.want) {
				t.Fatalf("Reason %q nao nomeia o motivo (esperado conter %q); degradacao silenciosa e amplificador",
					frontier.Reason, tc.want)
			}
		})
	}
}

// TestItemsWithoutANumberAreDroppedAndCounted: `numero` absent decodes to 0,
// and keying on it would invent a phantom card the board would then rank.
func TestItemsWithoutANumberAreDroppedAndCounted(t *testing.T) {
	stdout := `{"pronto":[{"titulo":"sem numero","rank":1},{"titulo":"ok","numero":5,"rank":2}],"bloqueado":[]}`
	frontier, err := LoadFrontier(fakeRunner(t, stdout, nil, nil), "bin/kit.py", "bugtoprompt")
	if err != nil {
		t.Fatalf("LoadFrontier devolveu erro: %v", err)
	}
	if _, ok := frontier.Priority[0]; ok {
		t.Fatalf("item sem numero virou a chave 0 do mapa; numero ausente nao e a issue 0")
	}
	if len(frontier.Priority) != 1 || !frontier.Priority[5].Has {
		t.Fatalf("mapa voltou %+v; esperado so a issue 5", frontier.Priority)
	}
	if !strings.Contains(frontier.Reason, "1 item(ns) sem numero") {
		t.Fatalf("Reason %q nao conta o item descartado", frontier.Reason)
	}
}

// TestMisWiredCallErrorsButStillReturnsUsableMaps: no runner, no kit path, no
// slug is a bug in the caller, not a source that went quiet — it earns an
// error. The maps stay indexable anyway so a caller that logs and continues
// cannot panic.
func TestMisWiredCallErrorsButStillReturnsUsableMaps(t *testing.T) {
	cases := []struct {
		name    string
		run     Runner
		kitPath string
		slug    string
	}{
		{name: "sem runner", run: nil, kitPath: "bin/kit.py", slug: "bugtoprompt"},
		{name: "sem kit", run: fakeRunner(t, kitAnswer, nil, nil), kitPath: "  ", slug: "bugtoprompt"},
		{name: "sem slug", run: fakeRunner(t, kitAnswer, nil, nil), kitPath: "bin/kit.py", slug: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frontier, err := LoadFrontier(tc.run, tc.kitPath, tc.slug)
			if err == nil {
				t.Fatalf("chamada mal fiada (%s) passou calada", tc.name)
			}
			if frontier.Reason == "" {
				t.Fatalf("chamada mal fiada (%s) voltou sem Reason", tc.name)
			}
			frontier.Priority[1] = Priority{Has: true}
			frontier.Blocked[1] = true
		})
	}
}

// The deadline is the one behaviour in this package that no fixture can prove:
// it needs a real child that outlives its context. This test binary is that
// child. Re-execing it keeps the rule hermetic — nothing here runs python3,
// kit.py, gh, or usina, and nothing depends on an external binary being on
// PATH — while still going through the same exec.CommandContext the board uses.
const (
	helperEnv     = "FILA_TEST_CHILD"
	helperSleeps  = "dorme"
	helperRefuses = "recusa"
	// helperRefusal stands in for kit.py's own refusal, the text that must
	// survive to the board when the child fails on its own terms.
	helperRefusal = "fronteira: nao consegui ler as issues de usina-fonte-unica"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case helperSleeps:
		// Long enough that only the deadline can end it.
		time.Sleep(time.Minute)
		os.Exit(0)
	case helperRefuses:
		fmt.Fprintln(os.Stderr, helperRefusal)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// helperChild arms this binary to behave as mode when re-exec'd and returns the
// path to run. The mode travels in the environment because ExecRunner passes no
// environment of its own: the child inherits ours.
func helperChild(t *testing.T, mode string) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("nao achei o binario de teste para usar como filho: %v", err)
	}
	t.Setenv(helperEnv, mode)
	return path
}

// TestDeadlineNamesTheDeadlineAndItsValue: a child killed by the context exits
// -1 with empty stderr, so the board printed "python3 saiu -1: signal: killed"
// (measured on the owner's real project). That names neither the deadline nor
// its value, so nobody reading the board can tell what to change.
func TestDeadlineNamesTheDeadlineAndItsValue(t *testing.T) {
	child := helperChild(t, helperSleeps)
	const prazo = 150 * time.Millisecond

	stdout, err := execRunnerWithTimeout(prazo, child)
	if err == nil {
		t.Fatalf("filho que estourou o prazo voltou sem erro (stdout %q)", stdout)
	}
	msg := err.Error()
	if !strings.Contains(msg, "prazo") {
		t.Fatalf("erro %q nao diz que foi prazo", msg)
	}
	if !strings.Contains(msg, prazo.String()) {
		t.Fatalf("erro %q nao nomeia o valor do prazo (%s)", msg, prazo)
	}
	if strings.Contains(msg, "signal: killed") {
		t.Fatalf("erro %q repassou \"signal: killed\": e o texto que nao explica nada a quem le o board", msg)
	}
}

// TestChildThatFailsOnItsOwnKeepsItsDiagnosis: kit.py exiting 2 with its own
// stderr is a different failure from a deadline kill, and the useful part is
// the child's text plus its exit code. Answering that with deadline wording
// would send the reader after the wrong defect.
func TestChildThatFailsOnItsOwnKeepsItsDiagnosis(t *testing.T) {
	child := helperChild(t, helperRefuses)

	_, err := execRunnerWithTimeout(10*time.Second, child)
	if err == nil {
		t.Fatalf("filho que saiu 2 voltou sem erro")
	}
	msg := err.Error()
	if !strings.Contains(msg, helperRefusal) {
		t.Fatalf("erro %q perdeu o stderr do filho (%q)", msg, helperRefusal)
	}
	if !strings.Contains(msg, "saiu 2") {
		t.Fatalf("erro %q nao carrega o exit code 2 do filho", msg)
	}
	if strings.Contains(msg, "prazo") {
		t.Fatalf("erro %q culpou o prazo numa falha que o filho diagnosticou sozinho", msg)
	}
}

// TestProductionDeadlineClearsTheMeasuredFrontierCost pins the const against
// the measurement that condemned the old value: `kit.py fronteira
// usina-fonte-unica` answered in 69s — measured with `time`, one isolated
// successful call, the owner's real project with 33 open cards. The 60s
// deadline that shipped before sat below that and killed every refresh, so
// lowering this back under the measurement reintroduces the defect.
func TestProductionDeadlineClearsTheMeasuredFrontierCost(t *testing.T) {
	const medido = 69 * time.Second
	if execTimeout <= medido {
		t.Fatalf("execTimeout e %s, nao passa dos %s medidos em `kit.py fronteira usina-fonte-unica`: o board volta a matar a fronteira",
			execTimeout, medido)
	}
	if execTimeout > 10*time.Minute {
		t.Fatalf("execTimeout e %s: um board que pendura tanto antes de reportar diz menos que um que reporta", execTimeout)
	}
}
