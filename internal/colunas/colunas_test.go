package colunas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openStore(t *testing.T, dir, boardID string) *Store {
	t.Helper()
	store, err := Open(dir, boardID)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// The acceptance criterion in prose: an issue moved to Triage is still in
// Triage after hvb restarts. A restart is a fresh Open over the same data
// directory and board id, so that is exactly what this reopens.
func TestSetSurvivesReopeningTheSameBoard(t *testing.T) {
	dir := t.TempDir()
	boardID := BoardID("aryrabelo/bugtoprompt", "/vault")

	first := openStore(t, dir, boardID)
	if err := first.Set(Entry{ID: "issue-9f2c", Repo: "aryrabelo/bugtoprompt", Issue: 12, Column: "triage", SetBy: "ary"}); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "columns", boardID+".json")
	if first.Path() != want {
		t.Errorf("Path() = %s, want %s", first.Path(), want)
	}

	second := openStore(t, dir, boardID)
	columns, err := second.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if columns["issue-9f2c"] != "triage" {
		t.Fatalf("after reopening, column for issue-9f2c = %q, want triage", columns["issue-9f2c"])
	}

	entries, err := second.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries after reopening, want 1", len(entries))
	}
	if entries[0].Issue != 12 || entries[0].Repo != "aryrabelo/bugtoprompt" || entries[0].SetBy != "ary" {
		t.Errorf("entry lost fields across the reopen: %+v", entries[0])
	}
}

func TestBoardIDIgnoresOrderAndWhitespace(t *testing.T) {
	base := BoardID("aryrabelo/bugtoprompt", "/vault")
	if reordered := BoardID("/vault", "aryrabelo/bugtoprompt"); reordered != base {
		t.Errorf("BoardID depends on argument order: %s vs %s", base, reordered)
	}
	if padded := BoardID("  aryrabelo/bugtoprompt ", "\t/vault\n"); padded != base {
		t.Errorf("BoardID depends on surrounding whitespace: %s vs %s", base, padded)
	}
	if withEmpty := BoardID("aryrabelo/bugtoprompt", "   ", "/vault"); withEmpty != base {
		t.Errorf("an unset source forked the board id: %s vs %s", base, withEmpty)
	}
	if !strings.HasPrefix(base, "queue-") || len(base) != len("queue-")+16 {
		t.Errorf("BoardID = %q, want queue- plus 16 hex digits", base)
	}
}

func TestBoardIDDiffersForDifferentSources(t *testing.T) {
	both := BoardID("aryrabelo/bugtoprompt", "/vault")
	for name, other := range map[string]string{
		"one source dropped":   BoardID("aryrabelo/bugtoprompt"),
		"different repository": BoardID("aryrabelo/other", "/vault"),
		"no sources at all":    BoardID(),
	} {
		if other == both {
			t.Errorf("%s collided with the two-source board: %s", name, other)
		}
	}
}

func TestSetUpsertsByID(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	if err := store.Set(Entry{ID: "card-1", Column: "triage"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(Entry{ID: "card-1", Column: "planning"}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries for one id, want 1: %+v", len(entries), entries)
	}
	if entries[0].Column != "planning" {
		t.Errorf("column = %q, want the last value planning", entries[0].Column)
	}
}

func TestSetRefusesAnEntryWithoutIDOrColumn(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	if err := store.Set(Entry{Column: "triage"}); err == nil {
		t.Error("Set accepted an entry with no id")
	}
	err := store.Set(Entry{ID: "card-1"})
	if err == nil {
		t.Fatal("Set accepted an entry with no column")
	}
	if !strings.Contains(err.Error(), "Clear") {
		t.Errorf("error must name the fix, got: %v", err)
	}
}

func TestSetStampsAnEntryThatArrivesWithoutATime(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	before := time.Now().Add(-time.Second)
	if err := store.Set(Entry{ID: "card-1", Column: "triage"}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want the one just set", len(entries))
	}
	if entries[0].SetAt.Before(before) {
		t.Errorf("SetAt = %s, want a stamp from now", entries[0].SetAt)
	}
}

func TestClearForgetsACardAndIgnoresAnAbsentID(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	if err := store.Set(Entry{ID: "card-1", Column: "triage"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear("card-2"); err != nil {
		t.Errorf("Clear on an absent id must not error: %v", err)
	}
	if err := store.Clear("card-1"); err != nil {
		t.Fatal(err)
	}
	columns, err := store.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := columns["card-1"]; ok {
		t.Error("card-1 still stored after Clear")
	}
}

func TestListIsOrderedByIDAndByteStable(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	for _, id := range []string{"card-c", "card-a", "card-b"} {
		if err := store.Set(Entry{ID: id, Column: "triage", SetAt: time.Unix(0, 0).UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("got %d entries, want 3", len(first))
	}
	ids := []string{first[0].ID, first[1].ID, first[2].ID}
	if ids[0] != "card-a" || ids[1] != "card-b" || ids[2] != "card-c" {
		t.Fatalf("List is not ordered by id: %v", ids)
	}
	second, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Errorf("two reads of an unchanged store differ:\n%s\n%s", a, b)
	}
}

func TestDocumentFromTheFutureIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	boardID := BoardID("repo")
	store := openStore(t, dir, boardID)
	future := `{"version":99,"board":"` + boardID + `","entries":[{"id":"card-1","column":"who-knows"}]}` + "\n"
	if err := os.WriteFile(store.Path(), []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("a file from the future must not be an error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries from version 99 were parsed anyway: %+v", entries)
	}

	if err := store.Set(Entry{ID: "card-2", Column: "triage"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != version {
		t.Errorf("rewritten file has version %d, want %d", doc.Version, version)
	}
	if len(doc.Entries) != 1 || doc.Entries[0].ID != "card-2" {
		t.Errorf("the future document survived the rewrite: %+v", doc.Entries)
	}
}

func TestCorruptFileStartsCleanInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir, BoardID("repo"))
	if err := os.WriteFile(store.Path(), []byte("{\"version\": 1, \"entries\": [trunc"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("a corrupt file must not blank the board with an error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries out of a corrupt file", len(entries))
	}
	if err := store.Set(Entry{ID: "card-1", Column: "triage"}); err != nil {
		t.Fatalf("the store stayed unusable after a corrupt read: %v", err)
	}
	columns, err := store.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if columns["card-1"] != "triage" {
		t.Errorf("column for card-1 = %q, want triage", columns["card-1"])
	}
}

func TestOpenRefusesAMissingDirectoryOrBoardID(t *testing.T) {
	if _, err := Open("", BoardID("repo")); err == nil {
		t.Error("Open accepted an empty data directory")
	}
	if _, err := Open(t.TempDir(), "  "); err == nil {
		t.Error("Open accepted an empty board id")
	}
}

func TestConcurrentSetsDoNotLoseEachOther(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	ids := []string{"card-0", "card-1", "card-2", "card-3", "card-4", "card-5", "card-6", "card-7"}
	var wg sync.WaitGroup
	errs := make([]error, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			errs[i] = store.Set(Entry{ID: id, Column: "triage"})
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Set(%s): %v", ids[i], err)
		}
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(ids) {
		t.Fatalf("got %d entries after %d concurrent writes, want %d", len(entries), len(ids), len(ids))
	}
}
