package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// appWithRuns builds the smallest App resolveRun needs: a run store holding the
// given run ids, under a temporary data directory.
func appWithRuns(t *testing.T, ids ...string) *App {
	t.Helper()
	t.Setenv("HVB_DATA_DIR", t.TempDir())
	store, err := runs.Open("project")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		run := &runs.Run{
			ID: id, FeatureID: strings.ToUpper(id), Title: id,
			Status: feature.InProgress, State: runs.Running,
			PaneID: "w1:p1", StartedAt: time.Now().UTC(),
		}
		if err := store.Append(run); err != nil {
			t.Fatal(err)
		}
	}
	return &App{store: store}
}

// VB-07. The contract hvb embeds in every prompt tells a dispatched agent to
// report with a bare `hvb run done`, and $HVB_RUN_ID is how hvb knows which run
// that is. A command naming a different run from inside a dispatched pane is
// therefore one run reaching for another — which would let it close a sibling
// as succeeded, move that feature on, or publish its branch.
func TestResolveRunRefusesARunOtherThanThePanesOwn(t *testing.T) {
	app := appWithRuns(t, "ftr-0001-a", "ftr-0002-b")
	t.Setenv("HVB_RUN_ID", "ftr-0001-a")

	run, err := resolveRun(app, []string{"ftr-0002-b"})
	if err == nil {
		t.Fatalf("a dispatched pane resolved a foreign run: %+v", run)
	}
	if !strings.Contains(err.Error(), "ftr-0001-a") || !strings.Contains(err.Error(), "ftr-0002-b") {
		t.Errorf("the refusal must name both runs: %v", err)
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("ExitCode = %d, want %d — this is caller input, not a board failure", ExitCode(err), ExitUsage)
	}
}

// The other half: nothing the operator or the agent legitimately does breaks.
func TestResolveRunAcceptsTheUsualPaths(t *testing.T) {
	t.Run("the pane's own id, spelled out", func(t *testing.T) {
		app := appWithRuns(t, "ftr-0001-a")
		t.Setenv("HVB_RUN_ID", "ftr-0001-a")
		run, err := resolveRun(app, []string{" ftr-0001-a "})
		if err != nil {
			t.Fatalf("an agent naming its own run must be allowed: %v", err)
		}
		if run.ID != "ftr-0001-a" {
			t.Errorf("run = %q", run.ID)
		}
	})

	t.Run("no id at all, from a dispatched pane", func(t *testing.T) {
		app := appWithRuns(t, "ftr-0001-a", "ftr-0002-b")
		t.Setenv("HVB_RUN_ID", "ftr-0002-b")
		run, err := resolveRun(app, nil)
		if err != nil {
			t.Fatal(err)
		}
		if run.ID != "ftr-0002-b" {
			t.Errorf("run = %q, want the pane's own", run.ID)
		}
	})

	t.Run("an operator's shell, with no run id in the environment", func(t *testing.T) {
		app := appWithRuns(t, "ftr-0001-a", "ftr-0002-b")
		t.Setenv("HVB_RUN_ID", "")
		run, err := resolveRun(app, []string{"ftr-0002-b"})
		if err != nil {
			t.Fatalf("the operator may address any run they like: %v", err)
		}
		if run.ID != "ftr-0002-b" {
			t.Errorf("run = %q", run.ID)
		}
	})
}

// A store path is derived from the workspace identity, so the temporary data
// directory must actually be where the runs landed. Without this the tests
// above could pass against a store that was never written.
func TestRunStoreLandsInTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HVB_DATA_DIR", dir)
	store, err := runs.Open("project")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "runs", "project.json"); store.Path() != want {
		t.Errorf("store path = %q, want %q", store.Path(), want)
	}
}
