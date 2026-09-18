package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/colunas"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// runState drives `hvb state show` the way a script does: through the real
// command tree, so SilenceUsage holds and stdout carries the contract and
// nothing else. A bare sub-command would print cobra's usage block to stdout on
// a bad flag, which the root exists to suppress.
func runState(t *testing.T, app *App, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app.Out, app.Err = &out, &errOut
	root := NewRoot(app)
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// The contract of ceo-bora#321: an array on stdout with the column of each
// card, the same shape the usina's listing verbs have, and nothing written to
// GitHub to produce it.
func TestStateShowEmitsAJSONArrayOfStoredColumns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HVB_DATA_DIR", dir)
	root, err := runs.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := colunas.Open(root, colunas.BoardID("aryrabelo/ceo-bora"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []colunas.Entry{
		{ID: "ceo-bora#321", Repo: "aryrabelo/ceo-bora", Issue: 321, Column: "triage", SetBy: "ary"},
		{ID: "ceo-bora#307", Repo: "aryrabelo/ceo-bora", Issue: 307, Column: "ready-to-review", SetBy: "ary"},
	} {
		if err := store.Set(entry); err != nil {
			t.Fatal(err)
		}
	}

	out, _, err := runState(t, &App{}, "state", "show", "--json", "--repo", "aryrabelo/ceo-bora")
	if err != nil {
		t.Fatalf("hvb state show failed: %v", err)
	}

	var rows []stateEntry
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %s", len(rows), out)
	}
	columns := map[string]string{}
	for _, row := range rows {
		columns[row.ID] = row.Column
		if row.SetAt == "" {
			t.Errorf("%s has no set_at, so nobody can tell a fresh move from a stale one", row.ID)
		}
	}
	if columns["ceo-bora#321"] != "triage" {
		t.Errorf("ceo-bora#321 reads %q, want triage", columns["ceo-bora#321"])
	}
	if columns["ceo-bora#307"] != "ready-to-review" {
		t.Errorf("ceo-bora#307 reads %q, want ready-to-review", columns["ceo-bora#307"])
	}
	if rows[0].Issue != 307 {
		t.Errorf("rows are not ordered by id, so two reads of one store differ: %s", out)
	}
}

// An empty store is `[]`, never `null`. A caller parsing stdout
// unconditionally — which is the point of the contract — would fail on null,
// and "no card has been moved" is not an error.
func TestStateShowEmitsAnEmptyArrayForAnUntouchedRepository(t *testing.T) {
	t.Setenv("HVB_DATA_DIR", t.TempDir())

	out, _, err := runState(t, &App{}, "state", "show", "--json", "--repo", "aryrabelo/ceo-bora")
	if err != nil {
		t.Fatalf("hvb state show failed on an untouched repository: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("stdout is %q, want []", strings.TrimSpace(out))
	}
}

// Two repositories are two stores. A shared file would hand a caller the
// columns of a queue it never asked about, joined onto its own issue numbers.
func TestStateShowKeepsRepositoriesApart(t *testing.T) {
	t.Setenv("HVB_DATA_DIR", t.TempDir())
	root, err := runs.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := colunas.Open(root, colunas.BoardID("aryrabelo/ceo-bora"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(colunas.Entry{ID: "ceo-bora#321", Column: "triage"}); err != nil {
		t.Fatal(err)
	}

	out, _, err := runState(t, &App{}, "state", "show", "--json", "--repo", "aryrabelo/ceo-pp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("ceo-pp was handed ceo-bora's columns: %s", out)
	}
}

// --repo is the key, so its absence is a usage error rather than a guess at
// which queue was meant.
func TestStateShowRefusesWithoutARepository(t *testing.T) {
	t.Setenv("HVB_DATA_DIR", t.TempDir())

	for _, args := range [][]string{
		{"show"},
		{"show", "--repo", "ceo-bora"},
		{"show", "--repo", "a/b/c"},
	} {
		out, _, err := runState(t, &App{}, append([]string{"state", "--json"}, args...)...)
		if err == nil {
			t.Fatalf("%v was accepted", args)
		}
		var usage UsageError
		if !asUsage(err, &usage) {
			t.Errorf("%v failed with %T, want a usage error so the exit code is 64", args, err)
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("%v wrote %q to stdout; a failure must leave it empty", args, out)
		}
	}
}

func asUsage(err error, target *UsageError) bool {
	if candidate, ok := err.(UsageError); ok {
		*target = candidate
		return true
	}
	return false
}
