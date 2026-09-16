package roles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/netors/herdr-virtualboard/internal/feature"
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
		role, ok := Suggest(loaded, spec, "fullstack_dev")
		if !ok || role.Key != want {
			t.Errorf("label %q suggested %q, want %q", label, role.Key, want)
		}
	}
}

// An explicit `role:` label is the user saying exactly what they want and must
// beat both the hint table and the status default.
func TestSuggestPrefersAnExplicitRoleLabel(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0001", Status: feature.Review, Labels: []string{"backend", "role:devops_engineer"}}}
	role, ok := Suggest(loaded, spec, "fullstack_dev")
	if !ok || role.Key != "devops_engineer" {
		t.Fatalf("Suggest = %q, want devops_engineer", role.Key)
	}
}

// A feature in review is being checked, not built, whatever its labels say
// about the work that produced it.
func TestSuggestRoutesReviewToQA(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0001", Status: feature.Review, Labels: []string{"unmapped-label"}}}
	role, ok := Suggest(loaded, spec, "fullstack_dev")
	if !ok || role.Key != "qa" {
		t.Fatalf("Suggest for a review feature = %q, want qa", role.Key)
	}
}

func TestSuggestFallsBackToTheConfiguredDefault(t *testing.T) {
	loaded, _ := Load(agentsDir(t))
	spec := &feature.Spec{Frontmatter: feature.Frontmatter{
		ID: "FTR-0001", Status: feature.InProgress, Labels: []string{"nothing-maps-here"}}}
	role, ok := Suggest(loaded, spec, "devops_engineer")
	if !ok || role.Key != "devops_engineer" {
		t.Fatalf("Suggest = %q, want the configured fallback", role.Key)
	}
}

func TestSuggestWithNoRolesReportsNotFound(t *testing.T) {
	if _, ok := Suggest(nil, nil, "backend_dev"); ok {
		t.Fatal("Suggest over an empty charter set should report not found")
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
