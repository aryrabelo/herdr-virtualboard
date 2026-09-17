package fila

import (
	"errors"
	"strings"
	"testing"
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
