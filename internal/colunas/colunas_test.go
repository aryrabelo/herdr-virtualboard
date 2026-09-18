package colunas

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// A writer that died — killed, panicked, the machine cut — leaves its lock
// file behind with a fresh mtime and nothing holding it. Waiting for that file
// to look old before touching it makes the board unwritable for the length of
// the staleness window; asking the kernel who holds it answers immediately.
func TestALockLeftByADeadWriterIsTakenImmediately(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	lock := store.Path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	// The pid of a process that is not there, and a mtime of right now:
	// the file is indistinguishable from a live lock by inspection alone.
	if err := os.WriteFile(lock, []byte("4194303\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- store.Set(Entry{ID: "card-dead", Column: "triage"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Set over a lock nobody holds: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a lock file no process holds blocked a writer: crash recovery must not wait for the file to look old")
	}

	columns, err := store.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if columns["card-dead"] != "triage" {
		t.Fatalf("column for card-dead = %q, want triage", columns["card-dead"])
	}
}

// The other half, and the bug: a writer suspended after taking the lock — a
// sleeping laptop, a loaded machine, a debugger stopped at a breakpoint — used
// to have its LIVE lock deleted by the next writer the moment the file looked
// old enough, and the two then read the same document and raced to rename
// their snapshots over each other, losing a move. A lease the kernel owns
// cannot be reclaimed while its owner lives, however old the file looks.
func TestALiveLockIsNotReclaimedHoweverOldItLooks(t *testing.T) {
	store := openStore(t, t.TempDir(), BoardID("repo"))
	lock := store.Path() + ".lock"
	release := holdLockInAnotherProcess(t, "^TestHelperHoldsTheColumnLock$", holdLockEnv, lock)

	// Aged well past any staleness window a reader could invent.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- store.Set(Entry{ID: "card-live", Column: "triage"}) }()
	select {
	case err := <-done:
		t.Fatalf("Set went through (err=%v) while another process held the lock: a stale-looking lock whose owner is alive must not be reclaimed", err)
	case <-time.After(300 * time.Millisecond):
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Set after the holder exited: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Set never took the lock the holder released")
	}
}

// holdLockInAnotherProcess starts a child holding a lock and returns the
// function that makes it let go. runPattern names which child half to
// re-execute and env carries what it needs to lock.
//
// It has to be another PROCESS: an flock belongs to an open file description,
// so a second descriptor opened here would be granted the same lock and prove
// nothing about a foreign holder. For the same reason a goroutine proves
// nothing either — Store serialises its own writers on a sync.Mutex, so an
// in-process race never reaches flock at all.
func holdLockInAnotherProcess(t *testing.T, runPattern, env, value string) func() {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+runPattern)
	cmd.Env = append(os.Environ(), env+"="+value)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		// Closing stdin is the signal; the kernel drops the lock when
		// the child's descriptors close with it.
		stdin.Close()
		_ = cmd.Wait()
	}
	t.Cleanup(release)

	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			release()
			t.Fatalf("the child never reported holding %s: %v", value, err)
		}
		if strings.Contains(line, holdLockReady) {
			return release
		}
	}
}

const (
	holdLockEnv      = "HVB_TEST_HOLD_COLUMN_LOCK"
	holdStoreLockEnv = "HVB_TEST_HOLD_STORE_LOCK"
	holdLockReady    = "column-lock-held"
)

// TestTwoWritersCannotHoldTheStoreLockAtOnce is the one test that can see the
// lock MODE, and it exists because nothing else could.
//
// TestALiveLockIsNotReclaimedHoweverOldItLooks has its child take LOCK_EX
// directly, so it stays green even if the store itself downgraded to a shared
// lock: the child's exclusive lock excludes a shared waiter too. And
// TestConcurrentSetsDoNotLoseEachOther runs goroutines in one process, where
// Store's sync.Mutex serialises every writer before flock is consulted. So
// both pass with LOCK_SH in lockFile, which is two writers publishing over
// each other on any real two-pane board.
//
// Here BOTH sides take the lock through the production path. Shared locks are
// compatible with each other, so a downgrade lets the second one in and this
// test is the thing that says so.
func TestTwoWritersCannotHoldTheStoreLockAtOnce(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir, BoardID("repo"))
	release := holdLockInAnotherProcess(t, "^TestHelperHoldsTheStoreLock$", holdStoreLockEnv, dir)
	defer release()

	unlock, err := store.lockFile()
	if err == nil {
		unlock()
		t.Fatal("the store handed out its lock twice across processes: a second writer reads the document the first one is about to replace, and whichever renames last publishes a board missing the other's move")
	}
	// The refusal has to name the file, because the operator's next move is
	// to find out who is holding it.
	if want := store.Path() + ".lock"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not name %s: %v", want, err)
	}
}

// TestHelperHoldsTheStoreLock is the child half of
// TestTwoWritersCannotHoldTheStoreLockAtOnce. Unlike TestHelperHoldsTheColumnLock
// it locks through Store.lockFile, which is the whole point: it holds exactly
// the lock production holds, in exactly the mode production asks for.
func TestHelperHoldsTheStoreLock(t *testing.T) {
	dir := os.Getenv(holdStoreLockEnv)
	if dir == "" {
		t.Skip("child half of TestTwoWritersCannotHoldTheStoreLockAtOnce")
	}
	store, err := Open(dir, BoardID("repo"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.lockFile()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fmt.Println(holdLockReady)
	// Hold it until the parent closes stdin.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// TestHelperHoldsTheColumnLock is not a test of its own: it is the child half
// of TestALiveLockIsNotReclaimedHoweverOldItLooks, re-executed from the test
// binary because that is the only process this package can start.
func TestHelperHoldsTheColumnLock(t *testing.T) {
	path := os.Getenv(holdLockEnv)
	if path == "" {
		t.Skip("child half of TestALiveLockIsNotReclaimedHoweverOldItLooks")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	fmt.Println(holdLockReady)
	// Hold it until the parent closes stdin.
	_, _ = io.Copy(io.Discard, os.Stdin)
}
