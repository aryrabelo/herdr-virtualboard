package fios

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fixedClock is the day the fixtures were written against, so the stale
// threshold is a property of the data and not of when the suite runs.
var fixedClock = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// card is the flattened view the assertions compare against; comparing whole
// feature.Spec values would drag in fields this source never sets.
type card struct {
	ID     string
	Title  string
	Status string
	Owner  string
	Labels string
	Path   string
	Body   string
}

func load(t *testing.T, root string) ([]*card, []error) {
	t.Helper()
	source := New(root)
	source.now = func() time.Time { return fixedClock }
	specs, errs := source.Load(context.Background())
	out := make([]*card, 0, len(specs))
	for _, spec := range specs {
		out = append(out, &card{
			ID:     spec.ID,
			Title:  spec.Title,
			Status: string(spec.Status),
			Owner:  spec.Owner,
			Labels: strings.Join(spec.Labels, ","),
			Path:   spec.Path,
			Body:   spec.Body,
		})
	}
	return out, errs
}

// byTitle finds a card the way a human reads the surface. Ids are content
// hashes, so hardcoding them in every assertion would only test sha256.
func byTitle(t *testing.T, specs []*card, title string) *card {
	t.Helper()
	for _, spec := range specs {
		if spec.Title == title {
			return spec
		}
	}
	t.Fatalf("card %q ausente; titulos lidos: %v", title, titles(specs))
	return nil
}

func TestEveryBoxStateMapsToTheContractedStatus(t *testing.T) {
	specs, errs := load(t, filepath.Join("testdata", "vault"))
	if len(errs) != 0 {
		t.Fatalf("fixture bem formada devolveu erros: %v", errs)
	}

	for _, want := range []struct {
		title  string
		status string
		owner  string
		labels string
	}{
		{"binario instalado na frota", "done", "", "hvb:source:gates,hvb:gate:factory-fix.md,hvb:gate-id:G1"},
		{"a fila le o vault do dono", "backlog", "", "hvb:source:gates,hvb:gate:factory-fix.md,hvb:gate-id:G2"},
		{"credencial readonly no cofre", "blocked", "Ary", "hvb:source:gates,hvb:gate:factory-fix.md,hvb:gate-id:G3"},
		// `- [-]` is resolved, not canceled (CONVENCAO.md:15).
		{"painel proprio em Grafana", "done", "", "hvb:source:gates,hvb:gate:factory-fix.md,hvb:gate-id:G4,hvb:resolved:other"},
	} {
		got := byTitle(t, specs, want.title)
		if got.Status != want.status {
			t.Errorf("%s: status %q, queria %q", got.ID, got.Status, want.status)
		}
		if got.Owner != want.owner {
			t.Errorf("%s: owner %q, queria %q", got.ID, got.Owner, want.owner)
		}
		if got.Labels != want.labels {
			t.Errorf("%s: labels %q, queria %q", got.ID, got.Labels, want.labels)
		}
	}
}

// A closed-without-delivery box must never borrow the label that means "this
// pull request died": the terminal column derived from `state:canceled` exists
// to separate a dead item from one solved another way, and mixing the two
// destroys the only distinction that column makes.
func TestNoGateBoxClaimsTheCanceledLabel(t *testing.T) {
	for _, root := range []string{"vault", "inserted", "malformed"} {
		specs, _ := load(t, filepath.Join("testdata", root))
		if len(specs) == 0 {
			t.Fatalf("%s: nenhuma card lida, o teste nao mediria nada", root)
		}
		for _, spec := range specs {
			if strings.Contains(spec.Labels, "state:canceled") {
				t.Errorf("%s: %s carrega state:canceled (labels %q); `- [-]` conta como RESOLVIDO (CONVENCAO.md:15)", root, spec.ID, spec.Labels)
			}
		}
	}

	specs, _ := load(t, filepath.Join("testdata", "vault"))
	resolved := byTitle(t, specs, "painel proprio em Grafana")
	if resolved.Status != "done" || !strings.Contains(resolved.Labels, "resolved:other") {
		t.Errorf("caixa `- [-]` devia ser done + resolved:other, veio status %q labels %q", resolved.Status, resolved.Labels)
	}
}

// Every label this source mints lives under `hvb:`. The prefix is what tells a
// consumer "hvb concluded this" apart from "a file said this", and a consumer
// derives a COLUMN from some of these — so an unprefixed label would let file
// content dictate where a card lands. This source reads no external label
// list (fixtures included: the bracketed context, the ledger file name and the
// box id are values appended AFTER a minted key), so minting in the namespace
// is the whole defence and no discard rule is needed.
func TestEveryMintedLabelIsInTheHvbNamespace(t *testing.T) {
	if LabelPrefix != "hvb:" {
		t.Fatalf("LabelPrefix = %q, o contrato do batch e \"hvb:\"", LabelPrefix)
	}
	seen := 0
	for _, root := range []string{"vault", "inserted", "malformed", "twins"} {
		specs, _ := load(t, filepath.Join("testdata", root))
		for _, spec := range specs {
			for _, label := range strings.Split(spec.Labels, ",") {
				if label == "" {
					continue
				}
				seen++
				if !strings.HasPrefix(label, LabelPrefix) {
					t.Errorf("%s: %s tem label fora do namespace: %q", root, spec.ID, label)
				}
			}
		}
	}
	if seen < 20 {
		t.Fatalf("so %d labels medidos; o teste nao esta vendo as fixtures", seen)
	}
}

var (
	reFioID       = regexp.MustCompile(`^FIO-[0-9a-f]{8}$`)
	reGateIDShape = regexp.MustCompile(`^GATE-[a-z0-9-]+-[0-9a-f]{8}$`)
)

// Inserting an item above the others must not touch the ids of the items
// below. `runs.Run.FeatureID` (internal/runs/store.go:57) binds a dispatched
// run to the card id, so a positional id would silently re-point a live run at
// a different thread the next time the owner adds a line at the top.
func TestIdsSurviveAnInsertionAbove(t *testing.T) {
	before, _ := load(t, filepath.Join("testdata", "vault"))
	after, _ := load(t, filepath.Join("testdata", "inserted"))

	if len(after) != len(before)+2 {
		t.Fatalf("fixture inserted/ devia ter 2 itens a mais (1 fio + 1 caixa), tem %d contra %d", len(after), len(before))
	}

	ids := make(map[string]string, len(after))
	for _, spec := range after {
		ids[spec.Title] = spec.ID
	}
	for _, spec := range before {
		got, ok := ids[spec.Title]
		if !ok {
			t.Errorf("%q desapareceu depois da insercao", spec.Title)
			continue
		}
		if got != spec.ID {
			t.Errorf("%q mudou de id com uma insercao acima: %s -> %s", spec.Title, spec.ID, got)
		}
	}

	// And the ids are the shape the contract promises, not an accident.
	for _, spec := range before {
		switch {
		case strings.Contains(spec.Labels, LabelSourceFios):
			if !reFioID.MatchString(spec.ID) {
				t.Errorf("id de fio fora de forma: %q", spec.ID)
			}
		case !reGateIDShape.MatchString(spec.ID):
			t.Errorf("id de gate fora de forma: %q", spec.ID)
		}
	}
}

// A gate box keyed on the id its owner declared (G7, I12) keeps its card when
// the title is reworded — the same run stays bound to the same box.
func TestGateBoxIDFollowsTheDeclaredIDNotTheTitle(t *testing.T) {
	first := boxID("factory-fix", "G3", "credencial readonly no cofre")
	reworded := boxID("factory-fix", "G3", "credencial readonly no cofre do 1Password")
	if first != reworded {
		t.Errorf("reescrever o titulo mudou o id da caixa G3: %s -> %s", first, reworded)
	}
	if other := boxID("factory-fix", "G4", "credencial readonly no cofre"); other == first {
		t.Errorf("G3 e G4 colidiram no mesmo id %s", first)
	}
	if otherLedger := boxID("jarvis", "G3", "credencial readonly no cofre"); otherLedger == first {
		t.Errorf("a mesma caixa em dois ledgers colidiu no mesmo id %s", first)
	}
}

// Two items with the same text in different files must not alias. A shared id
// is worse than a wrong id: `runs.Run.FeatureID` (internal/runs/store.go:57)
// would bind one run to two cards, and the board would show the same work
// twice with no way to tell which one is running.
func TestIdenticalTitlesInDifferentFilesGetDifferentIds(t *testing.T) {
	specs, _ := load(t, filepath.Join("testdata", "twins"))

	seen := map[string][]string{}
	for _, spec := range specs {
		seen[spec.ID] = append(seen[spec.ID], spec.Title+" @ "+filepath.Base(spec.Path))
	}
	for id, owners := range seen {
		if len(owners) > 1 {
			t.Errorf("id %s reivindicado por %d cards: %v", id, len(owners), owners)
		}
	}

	// The two ledgers hold a box with byte-identical text, and one with the
	// same declared id. Both pairs must separate inside the HASH, not only in
	// the `GATE-<slug>-` prefix: the prefix already differs, so comparing
	// whole ids would pass even with the ledger dropped from the material and
	// would prove nothing. The hash alone has to identify the box.
	if alfa, beta := hashPart(boxID("alfa", "", "rodar o gate")), hashPart(boxID("beta", "", "rodar o gate")); alfa == beta {
		t.Errorf("mesma caixa em alfa.md e beta.md tem o mesmo hash: %s", alfa)
	}
	if hashPart(boxID("alfa", "G1", "x")) == hashPart(boxID("beta", "G1", "y")) {
		t.Error("G1 de dois ledgers tem o mesmo hash")
	}
	// And the thread context separates two threads titled the same.
	if threadID("cnb", "Rodar o gate") == threadID("jarvis", "Rodar o gate") {
		t.Error("fios [cnb] e [jarvis] com o mesmo titulo colidiram")
	}
}

// The residual tie — same text, same file, same context — is broken in
// document order and must be identical on every Load. A map-ordered tiebreak
// would hand the same two cards swapped ids between two runs of the board.
func TestIdenticalTitlesInTheSameFileGetDistinctStableIds(t *testing.T) {
	first, firstErrs := load(t, filepath.Join("testdata", "twins"))
	second, _ := load(t, filepath.Join("testdata", "twins"))

	if len(first) != len(second) {
		t.Fatalf("dois Loads devolveram %d e %d cards", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Title != second[i].Title {
			t.Errorf("card %d instavel entre dois Loads: %s/%q contra %s/%q", i, first[i].ID, first[i].Title, second[i].ID, second[i].Title)
		}
	}

	// Two `[cnb] Rodar o gate` threads and two `rodar o gate` boxes in
	// alfa.md: the duplicates must exist as distinct cards…
	ids := map[string]bool{}
	dups := 0
	for _, spec := range first {
		if ids[spec.ID] {
			t.Errorf("id repetido depois do desempate: %s", spec.ID)
		}
		ids[spec.ID] = true
		if strings.HasSuffix(spec.ID, "-2") {
			dups++
		}
	}
	if dups != 2 {
		t.Errorf("esperava 2 cards desempatados com sufixo -2, achei %d; ids: %v", dups, idsOf(first))
	}
	// …and the duplication must be reported, because two identical lines in a
	// surface is a human mistake worth seeing.
	if !strings.Contains(joinErrs(firstErrs), "dois itens com o mesmo texto normalizado") {
		t.Errorf("duplicata nao reportada; erros: %s", joinErrs(firstErrs))
	}
}

// A thread keeps its id when it is closed: limpa strips the `~~` the owner
// wraps a closed thread in, so the hash is over the same normalised text.
func TestThreadIDSurvivesClosing(t *testing.T) {
	if open, closed := threadID("cnb", "**Quanto e o DAS num mes normal**"), threadID("cnb", "~~Quanto e o DAS num mes normal~~"); open != closed {
		t.Errorf("fechar o fio mudou o id: %s -> %s", open, closed)
	}
	if reflowed := threadID("cnb", "Quanto e o DAS   num  mes normal"); reflowed != threadID("cnb", "Quanto e o DAS num mes normal") {
		t.Errorf("reflow de espaco mudou o id: %s", reflowed)
	}
}

func TestBlockedBoxCarriesWhatUnblocksIt(t *testing.T) {
	specs, _ := load(t, filepath.Join("testdata", "vault"))
	got := byTitle(t, specs, "credencial readonly no cofre")
	if !strings.Contains(got.Body, "BLOQUEIO: Ary · mintar o token readonly") {
		t.Errorf("body sem a linha BLOQUEIO que declara o destravamento:\n%s", got.Body)
	}
	if !strings.Contains(got.Body, "CHECK:") || !strings.Contains(got.Body, "EXPECT:") {
		// CONVENCAO.md:19-20 — a blocked box keeps the CHECK/EXPECT it had; it
		// is what proves the unblock later without rewriting the gate.
		t.Errorf("body sem CHECK/EXPECT preservados:\n%s", got.Body)
	}
	if !filepath.IsAbs(got.Path) || !strings.HasSuffix(got.Path, filepath.Join("gates", "factory-fix.md")) {
		t.Errorf("Path %q nao e o caminho absoluto do ledger dono", got.Path)
	}
}

func TestClosedBoxBodyStopsAtTheNextHeading(t *testing.T) {
	specs, _ := load(t, filepath.Join("testdata", "vault"))
	got := byTitle(t, specs, "painel proprio em Grafana")
	if !strings.Contains(got.Body, "SUPERADO:") {
		t.Errorf("body sem o motivo do encerramento:\n%s", got.Body)
	}
	if strings.Contains(got.Body, "Rodape") || strings.Contains(got.Body, "Nada abaixo") {
		t.Errorf("body engoliu conteudo depois do heading:\n%s", got.Body)
	}
}

func TestTheConventionIsNotALedger(t *testing.T) {
	specs, _ := load(t, filepath.Join("testdata", "vault"))
	for _, spec := range specs {
		if strings.Contains(spec.Labels, "gate:CONVENCAO.md") || strings.Contains(spec.Title, "EXEMPLO") {
			t.Fatalf("caixa de gates/CONVENCAO.md virou card: %+v", spec)
		}
	}
}

func TestThreadStatusComesFromTheNextStepOwner(t *testing.T) {
	specs, errs := load(t, filepath.Join("testdata", "vault"))
	if len(errs) != 0 {
		t.Fatalf("fixture bem formada devolveu erros: %v", errs)
	}

	for _, want := range []struct {
		title  string
		status string
		owner  string
		labels string
	}{
		// Untriaged dump: nobody is parked on it, triaging is the work.
		{"ideia crua sem formato nenhum, so o despejo", "backlog", "", "hvb:source:fios,hvb:inbox"},
		// Inbox line already in the DEVE form stays a thread, plus `inbox`.
		{"Migrar a gemea desligada", "backlog", "eu", "hvb:source:fios,hvb:ctx:claudinha,hvb:inbox"},
		// Parked on a person → blocked, whoever the person is.
		{"Quanto e o DAS num mes normal", "blocked", "Karen", "hvb:source:fios,hvb:ctx:cnb"},
		// `DEVE: eu` is the agent's own debt, and 16 days old → stale.
		{"Watchdog de 4h que exercita o caminho REAL", "backlog", "eu", "hvb:source:fios,hvb:ctx:jarvis,hvb:stale"},
		// Shared owner list, and no DESDE at all → never stale.
		{"Sessao de brainstorm da ideia nova", "blocked", "Ary e Marina", "hvb:source:fios,hvb:ctx:pessoal"},
		// `## Fechados` is terminal and explicit (FIOS.md:21-22).
		{"Subfaturamento do Santorres", "done", "", "hvb:source:fios,hvb:closed"},
	} {
		got := byTitle(t, specs, want.title)
		if got.Status != want.status {
			t.Errorf("%s: status %q, queria %q", got.ID, got.Status, want.status)
		}
		if got.Owner != want.owner {
			t.Errorf("%s: owner %q, queria %q", got.ID, got.Owner, want.owner)
		}
		if got.Labels != want.labels {
			t.Errorf("%s: labels %q, queria %q", got.ID, got.Labels, want.labels)
		}
	}

	// The annotation indented under the closed thread is not a thread.
	for _, spec := range specs {
		if strings.Contains(spec.Title, "esta linha e anotacao") {
			t.Errorf("linha indentada virou card: %+v", spec)
		}
	}
}

func TestThreadBodyKeepsTheMeasuredDetail(t *testing.T) {
	specs, _ := load(t, filepath.Join("testdata", "vault"))
	got := byTitle(t, specs, "Quanto e o DAS num mes normal")
	want := "DESDE: 2026-09-12\nONDE: cnb-wiki ticket 18\nNOTA: a sobra de R$ 6.082/mes nao inclui imposto"
	if got.Body != want {
		t.Errorf("body\n%q\nqueria\n%q", got.Body, want)
	}
	if !filepath.IsAbs(got.Path) || !strings.HasSuffix(got.Path, "FIOS.md") {
		t.Errorf("Path %q nao e o FIOS.md absoluto", got.Path)
	}
}

func TestMalformedBoxesAreReportedWithoutLosingTheGoodOnes(t *testing.T) {
	specs, errs := load(t, filepath.Join("testdata", "malformed"))

	if got := byTitle(t, specs, "caixa acionavel bem formada, tem que sobreviver ao vizinho quebrado"); got.Status != "backlog" {
		t.Fatalf("a caixa bem formada nao sobreviveu ao vizinho quebrado: %+v", got)
	}
	// The blocked box stays on the board — it is open work — but with no owner,
	// because none was declared.
	blocked := byTitle(t, specs, "bloqueado sem dono declarado")
	if blocked.Status != "blocked" || blocked.Owner != "" {
		t.Fatalf("caixa `[~]` sem BLOQUEIO devia virar card bloqueado sem dono, veio %+v", blocked)
	}
	// An undefined marker is not a card at all: guessing a column would be
	// inventing state the convention does not define.
	for _, spec := range specs {
		if strings.Contains(spec.Title, "marcador que a convencao nao define") {
			t.Errorf("marcador desconhecido virou card: %+v", spec)
		}
	}

	wants := []string{
		"caixa `- [~]` sem BLOQUEIO:",
		"caixa `- [x]` sem EVIDENCE:",
		"marcador de caixa \"?\" desconhecido",
		// No FIOS.md in this fixture: a missing surface is one error, not a
		// dead batch.
		"superficie ilegivel",
	}
	joined := joinErrs(errs)
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Errorf("erro %q ausente; veio:\n%s", want, joined)
		}
	}
	for _, err := range errs {
		if !strings.Contains(err.Error(), "CONVENCAO.md:") && !strings.Contains(err.Error(), "superficie ilegivel") {
			t.Errorf("erro sem a citacao da convencao que ele viola: %v", err)
		}
	}
}

func TestLoadStopsOnACanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	specs, errs := New(filepath.Join("testdata", "vault")).Load(ctx)
	if len(specs) != 0 {
		t.Errorf("contexto cancelado devolveu %d cards", len(specs))
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "context canceled") {
		t.Errorf("erros %v, queria um context canceled", errs)
	}
}

func titles(specs []*card) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Title)
	}
	return out
}

func joinErrs(errs []error) string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return strings.Join(out, "\n")
}

func idsOf(specs []*card) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.ID)
	}
	return out
}

// hashPart is the trailing hash of a card id, without the `GATE-<slug>-`
// prefix, so an assertion can measure the hash material itself.
func hashPart(id string) string {
	if cut := strings.LastIndex(id, "-"); cut >= 0 {
		return id[cut+1:]
	}
	return id
}
