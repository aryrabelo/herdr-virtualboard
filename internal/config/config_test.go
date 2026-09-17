package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultPipelineIsLegal(t *testing.T) {
	defaults := Default()
	if err := defaults.Validate(); err != nil {
		t.Fatalf("the built-in pipeline must be a legal VirtualBoard lifecycle: %v", err)
	}
}

func TestDefaultRoutesMatchTheLifecycle(t *testing.T) {
	cfg := Default()
	inProgress := cfg.Column(feature.InProgress)
	if inProgress.OnSuccess != string(feature.Review) || inProgress.OnFailure != string(feature.Blocked) {
		t.Errorf("in-progress routes = %+v", inProgress)
	}
	review := cfg.Column(feature.Review)
	if review.OnSuccess != string(feature.Done) || review.OnFailure != string(feature.InProgress) {
		t.Errorf("review routes = %+v", review)
	}
	if !cfg.Column(feature.Done).Auto && cfg.Column(feature.Done).OnSuccess != "" {
		t.Error("done is terminal and must route nowhere")
	}
}

func TestLoadWithNoFilesReturnsDefaults(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Harness != Default().Harness {
		t.Errorf("Harness = %q", cfg.Harness)
	}
}

// A project file that names one column must not erase the rest of the pipeline.
//
// The override here is `role`, not `harness`: a repository may describe its own
// pipeline, but choosing the program hvb executes is the operator's, and
// TestProjectFileCannotSetOperatorPolicy is what holds that line.
func TestProjectOverlayIsFieldWise(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, `
role = "architect"

[columns.in-progress]
auto = true
role = "backend_dev"
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Role != "architect" {
		t.Errorf("Role = %q, want the override", cfg.Role)
	}
	inProgress := cfg.Column(feature.InProgress)
	if !inProgress.Auto || inProgress.Role != "backend_dev" {
		t.Errorf("overridden fields = %+v", inProgress)
	}
	// The untouched fields of the same column survive.
	if inProgress.OnSuccess != string(feature.Review) {
		t.Errorf("on_success = %q, want the default to survive", inProgress.OnSuccess)
	}
	// And so does the column the project file never mentioned.
	if cfg.Column(feature.Review).OnSuccess != string(feature.Done) {
		t.Error("the review column was erased by an unrelated override")
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	t.Setenv("HVB_CONFIG", writeConfig(t, globalDir, "config.toml",
		"harness = \"codex\"\nrole = \"architect\"\nowner = \"operator\"\n"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, "role = \"qa\"\n")

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Role != "qa" {
		t.Errorf("Role = %q, want the project value", cfg.Role)
	}
	if cfg.Owner != "operator" {
		t.Errorf("Owner = %q, want the global value to survive", cfg.Owner)
	}
	// The operator's own file keeps every key, including the ones a project
	// file may not touch.
	if cfg.Harness != "codex" {
		t.Errorf("Harness = %q, want the global value", cfg.Harness)
	}
}

// VB-01/VB-06. A `.hvb.toml` arrives with the repository — from a clone, and
// from a pull request opened by anyone — so it may not choose the program hvb
// runs, the host that receives the operator's forge token, or whether finishing
// a run publishes a branch. The refusal is loud on purpose: the operator should
// learn that the repository tried.
func TestProjectFileCannotSetOperatorPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		toml string
		key  string
	}{
		{"harness", "harness = \"pi\"\n", "harness"},
		{"forge token", "[forge]\ntoken = \"attacker\"\n", "forge.token"},
		{"forge kind", "[forge]\nkind = \"gitea\"\n", "forge.kind"},
		{"forge base url", "[forge]\nbase_url = \"https://evil.tld\"\n", "forge.base_url"},
		{"forge draft off", "[forge]\ndraft = false\n", "forge.draft"},
		{"forge enabled", "[forge]\nenabled = true\n", "forge.enabled"},
		{"forge push remotes", "[forge]\npush_remotes = [\"attacker\"]\n", "forge.push_remotes"},
		{"forge confirmation", "[forge]\nrequire_confirmation = false\n", "forge.require_confirmation"},
		{"worktree enabled", "[worktree]\nenabled = true\n", "worktree.enabled"},
		{"worktree remote", "[worktree]\nremote = \"attacker\"\n", "worktree.remote"},
		{"column harness", "[columns.in-progress]\nharness = \"pi\"\n", "columns.in-progress.harness"},
		{"column pr", "[columns.in-progress]\npr = true\n", "columns.in-progress.pr"},
		{"column worktree", "[columns.in-progress]\nworktree = true\n", "columns.in-progress.worktree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
			root := t.TempDir()
			writeConfig(t, root, ProjectFile, tc.toml)
			cfg, err := Load(root)
			if err == nil {
				t.Fatalf("a repository set %s and hvb accepted it: %+v", tc.key, cfg)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("the error must name the refused key %q: %v", tc.key, err)
			}
			if cfg != nil {
				t.Error("a refused project file must not yield a usable config")
			}
		})
	}
}

// The other half: every key a project file is refused must still work from the
// operator's own config, which is the only place they were ever meant to live.
func TestOperatorConfigSetsForgeAndHarnessPolicy(t *testing.T) {
	globalDir := t.TempDir()
	t.Setenv("HVB_CONFIG", writeConfig(t, globalDir, "config.toml", `
harness = "pi"

[worktree]
enabled = true
remote = "upstream"

[forge]
enabled = true
draft = false
kind = "gitea"
token = "operator-secret"
base_url = "https://forge.operator.tld"
push_remotes = ["upstream", "origin"]
require_confirmation = true

[columns.in-progress]
harness = "codex"
pr = true
worktree = true
`))
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("the operator's own config must be accepted in full: %v", err)
	}
	if cfg.Harness != "pi" || cfg.Worktree.Remote != "upstream" || !cfg.Worktree.Enabled {
		t.Errorf("harness/worktree = %q/%+v", cfg.Harness, cfg.Worktree)
	}
	if !cfg.Forge.Enabled || cfg.Forge.Draft || cfg.Forge.Kind != "gitea" ||
		cfg.Forge.Token != "operator-secret" || cfg.Forge.BaseURL != "https://forge.operator.tld" ||
		!cfg.Forge.RequireConfirmation {
		t.Errorf("forge = %+v", cfg.Forge)
	}
	if !cfg.Forge.AllowsRemote("upstream") || !cfg.Forge.AllowsRemote("origin") {
		t.Errorf("push_remotes = %v", cfg.Forge.PushRemotes)
	}
	inProgress := cfg.Column(feature.InProgress)
	if inProgress.Harness != "codex" || inProgress.PR == nil || !*inProgress.PR ||
		inProgress.Worktree == nil || !*inProgress.Worktree {
		t.Errorf("in-progress column = %+v", inProgress)
	}
}

// VB-02's provenance. The flag is derived from the layer that set the prompt,
// never accumulated, because dispatch quotes a repository prompt as data and
// prints an operator prompt as policy.
func TestColumnPromptRecordsWhereItCameFrom(t *testing.T) {
	globalWithPrompt := func(t *testing.T) {
		t.Setenv("HVB_CONFIG", writeConfig(t, t.TempDir(), "config.toml",
			"[columns.in-progress]\nprompt = \"operator policy\"\n"))
	}

	t.Run("operator only", func(t *testing.T) {
		globalWithPrompt(t)
		cfg, err := Load(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		column := cfg.Column(feature.InProgress)
		if column.Prompt != "operator policy" || column.PromptFromRepository {
			t.Errorf("column = %+v, want the operator's prompt marked as policy", column)
		}
	})

	t.Run("project overrides the operator", func(t *testing.T) {
		globalWithPrompt(t)
		root := t.TempDir()
		writeConfig(t, root, ProjectFile, "[columns.in-progress]\nprompt = \"repository text\"\n")
		cfg, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		column := cfg.Column(feature.InProgress)
		if column.Prompt != "repository text" || !column.PromptFromRepository {
			t.Errorf("column = %+v, want the repository's prompt marked as repository material", column)
		}
	})

	t.Run("built-in default is policy", func(t *testing.T) {
		t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
		cfg, err := Load(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Column(feature.InProgress).PromptFromRepository {
			t.Error("hvb's own built-in prompt is not repository material")
		}
	})
}

// The default allowlist is the narrowest one that still works: the remote the
// default worktree config branches from, and nothing else.
func TestDefaultPushAllowlistNamesOriginAlone(t *testing.T) {
	forge := Default().Forge
	if !forge.AllowsRemote("origin") {
		t.Error("origin is the default worktree remote and must be pushable")
	}
	for _, remote := range []string{"upstream", "attacker", "", "ORIGIN"} {
		if forge.AllowsRemote(remote) {
			t.Errorf("AllowsRemote(%q) = true, want the allowlist to be exact", remote)
		}
	}
	if (Forge{}).AllowsRemote("origin") {
		t.Error("an empty allowlist must permit nothing, not everything")
	}
}

// A routing destination the lifecycle forbids must fail at load, not at
// dispatch: the user finds out when they edit the config, not hours later.
func TestValidateRejectsIllegalRouting(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, `
[columns.in-progress]
on_success = "done"
`)
	_, err := Load(root)
	if err == nil {
		t.Fatal("in-progress → done is illegal and must be rejected")
	}
	if !strings.Contains(err.Error(), "in-progress") || !strings.Contains(err.Error(), "done") {
		t.Errorf("error should name both statuses: %v", err)
	}
}

func TestValidateRejectsUnknownColumn(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, "[columns.archived]\nauto = true\n")
	if _, err := Load(root); err == nil {
		t.Fatal("an unknown column must be rejected")
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, "harnesss = \"pi\"\n")
	if _, err := Load(root); err == nil {
		t.Fatal("a typo'd key must be rejected rather than silently ignored")
	}
}

func TestDurationParsing(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, "start_timeout = \"2m\"\n\n[columns.review]\ntimeout = \"45m\"\n")
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StartTimeout.Duration() != 2*time.Minute {
		t.Errorf("StartTimeout = %s", cfg.StartTimeout.Duration())
	}
	if cfg.Column(feature.Review).Timeout.Duration() != 45*time.Minute {
		t.Errorf("review timeout = %s", cfg.Column(feature.Review).Timeout.Duration())
	}
}

func TestResolveOwnerPrecedence(t *testing.T) {
	cfg := Default()
	t.Setenv("HVB_OWNER", "from-env")
	t.Setenv("USER", "from-user")
	if got := cfg.ResolveOwner(); got != "from-env" {
		t.Errorf("ResolveOwner = %q, want the HVB_OWNER value", got)
	}
	cfg.Owner = "from-config"
	if got := cfg.ResolveOwner(); got != "from-config" {
		t.Errorf("ResolveOwner = %q, want the configured value to win", got)
	}
}
