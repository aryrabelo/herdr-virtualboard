package roles

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/fila"
)

// agentsDir writes a charter set shaped like the one
// `.virtualboard/agents/` ships, including the two files that describe the
// system rather than a role.
func agentsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	charters := map[string]string{
		"backend_dev":                  "backend-dev|Backend APIs, databases, authentication, and server-side logic",
		"frontend_dev":                 "frontend-dev|UI components, responsive design, and client-side interactions",
		"fullstack_dev":                "fullstack-dev|End-to-end features",
		"qa":                           "qa|Testing, quality assurance, test automation",
		"devops_engineer":              "devops|CI/CD, infrastructure, deployment",
		"security_compliance_engineer": "security|Security reviews and threat modeling",
	}
	for key, value := range charters {
		name, description := value[:len(value)], ""
		for index := 0; index < len(value); index++ {
			if value[index] == '|' {
				name, description = value[:index], value[index+1:]
				break
			}
		}
		body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + key + "\n\nCharter body for " + key + ".\n"
		if err := os.WriteFile(filepath.Join(dir, key+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, notARole := range []string{"AGENTS.md", "RULES.md"} {
		if err := os.WriteFile(filepath.Join(dir, notARole), []byte("# system doc\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadSkipsSystemDocuments(t *testing.T) {
	loaded, err := Load(agentsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 6 {
		t.Fatalf("got %d roles, want 6 (AGENTS.md and RULES.md are not roles): %v", len(loaded), keys(loaded))
	}
	for _, role := range loaded {
		if role.Key == "AGENTS" || role.Key == "RULES" {
			t.Errorf("%s is a system document, not a role", role.Key)
		}
	}
}

func TestLoadReadsFrontmatter(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	role, found := Find(loaded, "backend_dev")
	if !found {
		t.Fatal("backend_dev not found")
	}
	if role.Name != "backend-dev" {
		t.Errorf("Name = %q, want the frontmatter spelling", role.Name)
	}
	if role.Description == "" {
		t.Error("Description should come from the frontmatter")
	}
}

// A workspace with no agents directory yields no roles and no error: dispatch
// then falls back to a role-less prompt rather than refusing to run.
func TestLoadMissingDirectoryIsNotAnError(t *testing.T) {
	loaded, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("got %d roles", len(loaded))
	}
}

func TestFindToleratesSpellingDifferences(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	for _, query := range []string{"backend_dev", "backend-dev", "BACKEND_DEV", " Backend-Dev "} {
		if _, found := Find(loaded, query); !found {
			t.Errorf("Find(%q) did not resolve", query)
		}
	}
	if _, found := Find(loaded, "nonexistent"); found {
		t.Error("Find should not invent a role")
	}
}

func TestSuggestFromLabels(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	cases := map[string]string{
		"backend":  "backend_dev",
		"api":      "backend_dev",
		"database": "backend_dev",
		"frontend": "frontend_dev",
		"ui":       "frontend_dev",
		"devops":   "devops_engineer",
		"security": "security_compliance_engineer",
		"test":     "qa",
	}
	for label, want := range cases {
		spec := &feature.Spec{Frontmatter: feature.Frontmatter{
			ID: "FTR-0001", Status: feature.InProgress, Labels: []string{label}}}
		role, err := Suggest(loaded, spec, "fullstack_dev")
		if err != nil || role.Key != want {
			t.Errorf("label %q suggested %q (err=%v), want %q", label, role.Key, err, want)
		}
	}
}

// queueAgentsDir writes the charter set the owner's queue board loads from
// `~/Sites/bora-team/ceo-bora/.virtualboard/agents`, in a temp directory: a
// test that read the real one would pass or fail with the owner's machine.
func queueAgentsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	charters := []struct{ key, name, description string }{
		{"executor", "executor", "Implementa uma folha ja fatiada contra o aceite escrito nela"},
		{"grilling", "grilling", "Afia plano ou decisao por interrogatorio, sem implementar"},
		{"cartografo", "cartografo", "Decompoe um esforco em folhas com aceite verificavel"},
	}
	for _, charter := range charters {
		body := "---\nname: " + charter.name + "\ndescription: " + charter.description +
			"\n---\n\n# " + charter.key + "\n\nCharter body for " + charter.key + ".\n"
		if err := os.WriteFile(filepath.Join(dir, charter.key+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# system doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// queueAgentsDirWithSentinel adds one charter no label and no hint names, so a
// Suggest test can pass it as the fallback. Without it, an answer that fell
// through the hint table would read as the role the case expected: the
// fallback for a `rumo:task` card is naturally `executor` too.
func queueAgentsDirWithSentinel(t *testing.T) string {
	t.Helper()
	dir := queueAgentsDir(t)
	body := "---\nname: sentinela\ndescription: nobody's hint points here\n---\n\n# sentinela\n"
	if err := os.WriteFile(filepath.Join(dir, "sentinela.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The owner's queue speaks `rumo:*`, not the upstream `frontend`/`backend`
// vocabulary: of the 33 issues labelled `project:bugtoprompt` in
// aryrabelo/ceo-bora, 16 carry `rumo:task`, 9 `rumo:grilling` and 2 `rumo:map`,
// and none carries any upstream hint label. Unmapped, every one of those cards
// got the same fallback role.
func TestSuggestFromTheOwnersRumoLabels(t *testing.T) {
	loaded, _ := Load(queueAgentsDirWithSentinel(t))
	cases := map[string]string{
		"rumo:task":     "executor",
		"rumo:grilling": "grilling",
		"rumo:map":      "cartografo",
	}
	for label, want := range cases {
		spec := &feature.Spec{Frontmatter: feature.Frontmatter{
			ID: "FTR-0001", Status: feature.InProgress, Labels: []string{"folha", label}}}
		role, err := Suggest(loaded, spec, "sentinela")
		if err != nil || role.Key != want {
			t.Errorf("label %q suggested %q (err=%v), want %q", label, role.Key, err, want)
		}
	}
}

// A `hitl` card is work only the owner's hands can close, so no charter fits
// it. The refusal has to beat the hints and the `role:` label both: an agent
// dispatched onto one would spend tokens on a task it cannot finish.
func TestSuggestRefusesHumanOnlyCards(t *testing.T) {
	loaded, _ := Load(queueAgentsDirWithSentinel(t))
	cases := map[string][]string{
		"hitl alone":               {"hitl"},
		"hitl before a hint":       {"hitl", "rumo:task"},
		"hitl after a hint":        {"folha", "rumo:task", "hitl"},
		"hitl with explicit role":  {"role:executor", "hitl"},
		"hitl in the board's case": {"HITL"},
	}
	for name, labels := range cases {
		spec := &feature.Spec{Frontmatter: feature.Frontmatter{
			ID: "FTR-0001", Status: feature.InProgress, Labels: labels}}
		role, err := Suggest(loaded, spec, "sentinela")
		if !errors.Is(err, ErrHumanOnly) {
			t.Errorf("%s: Suggest resolved %q (err=%v), want ErrHumanOnly (hitl is not dispatchable)", name, role.Key, err)
			continue
		}
		// The refusal is what both callers print, so it has to name the
		// card and the reason on its own: the board shows it verbatim.
		for _, want := range []string{"FTR-0001", fila.LabelHITL} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal %q does not name %q", name, err, want)
			}
		}
		// ErrNoCharter is the answer that dispatches under the default
		// role, so a refusal must never also read as one.
		if errors.Is(err, ErrNoCharter) {
			t.Errorf("%s: refusal also reads as ErrNoCharter: %v", name, err)
		}
	}
	// A charter set is not what makes a card human-only, so the refusal
	// stands even with no charters at all — otherwise this card would
	// answer ErrNoCharter, which dispatches under the configured role.
	bare := &feature.Spec{Frontmatter: feature.Frontmatter{ID: "FTR-0003", Labels: []string{"hitl"}}}
	if _, err := Suggest(nil, bare, "sentinela"); !errors.Is(err, ErrHumanOnly) {
		t.Errorf("hitl over an empty charter set = %v, want ErrHumanOnly", err)
	}
	// Control: the same charter set does resolve a card without `hitl`, so
	// the refusals above are the label's doing and not an inert fixture.
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0002", Status: feature.InProgress, Labels: []string{"folha", "rumo:task"}}}
	if role, err := Suggest(loaded, spec, "sentinela"); err != nil || role.Key != "executor" {
		t.Fatalf("control card suggested %q (err=%v), want executor", role.Key, err)
	}
}

// The explicit `role:` label still beats a hint on the owner's charter keys.
func TestSuggestPrefersAnExplicitOwnerRoleLabel(t *testing.T) {
	loaded, _ := Load(queueAgentsDir(t))
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0001", Status: feature.InProgress, Labels: []string{"rumo:task", "role:cartografo"}}}
	role, err := Suggest(loaded, spec, "executor")
	if err != nil || role.Key != "cartografo" {
		t.Fatalf("Suggest = %q (err=%v), want cartografo", role.Key, err)
	}
}

// Load over the owner's charter set answers exactly the three roles, with the
// frontmatter name and description of each, and not AGENTS.
func TestLoadTheOwnersCharterSet(t *testing.T) {
	loaded, err := Load(queueAgentsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cartografo", "executor", "grilling"}
	got := keys(loaded)
	if len(got) != len(want) {
		t.Fatalf("Load = %v, want %v", got, want)
	}
	for index, key := range want {
		if got[index] != key {
			t.Fatalf("Load = %v, want %v", got, want)
		}
		if loaded[index].Name != key {
			t.Errorf("%s: Name = %q, want the frontmatter name", key, loaded[index].Name)
		}
		if loaded[index].Description == "" {
			t.Errorf("%s: Description should come from the frontmatter", key)
		}
	}
}

// An explicit `role:` label is the user saying exactly what they want and must
// beat both the hint table and the status default.
func TestSuggestPrefersAnExplicitRoleLabel(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0001", Status: feature.Review, Labels: []string{"backend", "role:devops_engineer"}}}
	role, err := Suggest(loaded, spec, "fullstack_dev")
	if err != nil || role.Key != "devops_engineer" {
		t.Fatalf("Suggest = %q (err=%v), want devops_engineer", role.Key, err)
	}
}

// A feature in review is being checked, not built, whatever its labels say
// about the work that produced it.
func TestSuggestRoutesReviewToQA(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0001", Status: feature.Review, Labels: []string{"unmapped-label"}}}
	role, err := Suggest(loaded, spec, "fullstack_dev")
	if err != nil || role.Key != "qa" {
		t.Fatalf("Suggest for a review feature = %q (err=%v), want qa", role.Key, err)
	}
}

func TestSuggestFallsBackToTheConfiguredDefault(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0001", Status: feature.InProgress, Labels: []string{"nothing-maps-here"}}}
	role, err := Suggest(loaded, spec, "devops_engineer")
	if err != nil || role.Key != "devops_engineer" {
		t.Fatalf("Suggest = %q (err=%v), want the configured fallback", role.Key, err)
	}
}

// A charter set with nothing in it is not a refusal: it is ErrNoCharter, the
// answer dispatch turns into the configured default role.
func TestSuggestWithNoRolesReportsNoCharter(t *testing.T) {
	_, err := Suggest(nil, nil, "backend_dev")
	if !errors.Is(err, ErrNoCharter) {
		t.Fatalf("Suggest over an empty charter set = %v, want ErrNoCharter", err)
	}
	if errors.Is(err, ErrHumanOnly) {
		t.Fatalf("an empty charter set must not read as a refusal: %v", err)
	}
}

func TestCharterReadsTheFile(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	role, _ := Find(loaded, "qa")
	charter, err := role.Charter()
	if err != nil {
		t.Fatal(err)
	}
	if charter == "" {
		t.Fatal("Charter returned nothing")
	}
}

func keys(list []Role) []string {
	out := make([]string, len(list))
	for index, role := range list {
		out[index] = role.Key
	}
	return out
}

// This fails to compile if Suggest ever answers with anything but an error
// again. A bool second result is what made the refusal inert, because a
// dropped bool reads as "I do not care about a hint" and no reader or linter
// objects; this line is the cheapest possible lock on the shape.
var _ func([]Role, *feature.Spec, string) (Role, error) = Suggest

// The shape is only half the defence: a caller can still write `role, _ :=`.
// So this walks every call site in the module and fails on any that drops the
// second result, which is exactly the regression that shipped a green
// Suggest test alongside a board that dispatched human-only cards anyway.
//
// It is a lint expressed as a test because there is no other place in this
// module that runs one, and because the rule it defends is not observable in
// any single package's behaviour: it is a property of the call sites.
func TestNoCallerDiscardsTheRefusal(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	calls, offenders := 0, []string(nil)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch stmt := node.(type) {
			case *ast.AssignStmt:
				if len(stmt.Rhs) != 1 || !callsSuggest(stmt.Rhs[0]) {
					return true
				}
				calls++
				// One left-hand side means the second result was
				// never named, which Go only allows for a
				// single-result function — so a rewrite that
				// dropped the error entirely lands here too.
				if len(stmt.Lhs) < 2 {
					offenders = append(offenders, fmt.Sprintf("%s: Suggest called for its role alone", fset.Position(stmt.Pos())))
					return true
				}
				if name, ok := stmt.Lhs[1].(*ast.Ident); ok && name.Name == "_" {
					offenders = append(offenders, fmt.Sprintf("%s: the refusal is discarded into `_`", fset.Position(stmt.Pos())))
				}
			case *ast.ExprStmt:
				if callsSuggest(stmt.X) {
					calls++
					offenders = append(offenders, fmt.Sprintf("%s: Suggest called as a statement, discarding everything", fset.Position(stmt.Pos())))
				}
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	// Without this the guard would pass on a walk that found nothing —
	// a moved module root or a renamed function would read as compliance.
	if calls < 2 {
		t.Fatalf("found %d Suggest call sites under %s; the two real callers (tui and dispatch) plus this package's tests should all be there, so this guard proved nothing", calls, root)
	}
	for _, offender := range offenders {
		t.Errorf("%s (branch on roles.ErrHumanOnly and roles.ErrNoCharter instead)", offender)
	}
}

// callsSuggest matches both spellings: `Suggest` inside this package and
// `roles.Suggest` everywhere else.
func callsSuggest(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "Suggest"
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		return ok && pkg.Name == "roles" && fun.Sel.Name == "Suggest"
	}
	return false
}
