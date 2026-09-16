package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netors/herdr-virtualboard/internal/feature"
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
func TestProjectOverlayIsFieldWise(t *testing.T) {
	t.Setenv("HVB_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, `
harness = "pi"

[columns.in-progress]
auto = true
role = "backend_dev"
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Harness != "pi" {
		t.Errorf("Harness = %q, want the override", cfg.Harness)
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
	t.Setenv("HVB_CONFIG", writeConfig(t, globalDir, "config.toml", "harness = \"codex\"\nrole = \"architect\"\n"))
	root := t.TempDir()
	writeConfig(t, root, ProjectFile, "harness = \"pi\"\n")

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Harness != "pi" {
		t.Errorf("Harness = %q, want the project value", cfg.Harness)
	}
	if cfg.Role != "architect" {
		t.Errorf("Role = %q, want the global value to survive", cfg.Role)
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
