package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/netors/herdr-virtualboard/internal/feature"
)

// newWorkspace builds a minimal VirtualBoard layout: the five status
// directories under `.virtualboard/features/`.
func newWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, status := range feature.Statuses {
		if err := os.MkdirAll(filepath.Join(root, Dir, "features", string(status)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, Dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSpec(t *testing.T, root string, status feature.Status, id, title string, extra string) string {
	t.Helper()
	path := filepath.Join(root, Dir, "features", string(status), id+"-"+title+".md")
	body := "---\nid: " + id + "\ntitle: " + title + "\nstatus: " + string(status) +
		"\ncreated: 2026-01-01\nupdated: 2026-01-02\n" + extra + "---\n\n## Summary\nbody\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverWalksUp(t *testing.T) {
	root := newWorkspace(t)
	nested := filepath.Join(root, "src", "deep", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := Discover(nested)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !samePath(t, ws.Root, root) {
		t.Fatalf("Root = %q, want %q", ws.Root, root)
	}
}

func TestDiscoverReportsNotFound(t *testing.T) {
	_, err := Discover(t.TempDir())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Discover in an empty tree = %v; want ErrNotFound", err)
	}
}

// Tab-completing one directory too far is a common mistake and should still
// resolve to the project root.
func TestOpenAcceptsTheBoardDirectory(t *testing.T) {
	root := newWorkspace(t)
	ws, err := Open(filepath.Join(root, Dir))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !samePath(t, ws.Root, root) {
		t.Fatalf("Root = %q, want %q", ws.Root, root)
	}
}

// Two paths to the same workspace must produce one identity, because the run
// store keys its files on it.
func TestIDIsStableAcrossSymlinks(t *testing.T) {
	root := newWorkspace(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	direct, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	viaLink, err := Open(link)
	if err != nil {
		t.Fatal(err)
	}
	if direct.ID() != viaLink.ID() {
		t.Fatalf("ID differs through a symlink: %s vs %s", direct.ID(), viaLink.ID())
	}
}

func TestLoadSpecsGroupsByDirectory(t *testing.T) {
	root := newWorkspace(t)
	writeSpec(t, root, feature.Backlog, "FTR-0002", "second", "priority: P2\n")
	writeSpec(t, root, feature.Backlog, "FTR-0001", "first", "priority: P0\n")
	writeSpec(t, root, feature.Review, "FTR-0003", "third", "")

	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	specs, problems := ws.LoadSpecs()
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(specs) != 3 {
		t.Fatalf("got %d specs, want 3", len(specs))
	}
	// Backlog first (board order), P0 before P2 within it.
	if specs[0].ID != "FTR-0001" || specs[1].ID != "FTR-0002" || specs[2].ID != "FTR-0003" {
		t.Fatalf("order = %s %s %s", specs[0].ID, specs[1].ID, specs[2].ID)
	}
}

// The directory is the authority on status: a hand-edited spec whose
// frontmatter disagrees must still appear in the column it actually lives in.
func TestLoadSpecsTrustsDirectoryOverFrontmatter(t *testing.T) {
	root := newWorkspace(t)
	path := filepath.Join(root, Dir, "features", "review", "FTR-0009-stale.md")
	content := "---\nid: FTR-0009\ntitle: stale\nstatus: backlog\ncreated: 2026-01-01\nupdated: 2026-01-01\n---\n\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _ := Open(root)
	specs, _ := ws.LoadSpecs()
	if len(specs) != 1 || specs[0].Status != feature.Review {
		t.Fatalf("status = %v, want review (the directory it is in)", specs)
	}
}

// A spec that fails to parse must be reported, not silently dropped: a board
// that hides a feature is worse than one that shows an error.
func TestLoadSpecsReportsBrokenSpecs(t *testing.T) {
	root := newWorkspace(t)
	writeSpec(t, root, feature.Backlog, "FTR-0001", "good", "")
	broken := filepath.Join(root, Dir, "features", "backlog", "FTR-0002-broken.md")
	if err := os.WriteFile(broken, []byte("no frontmatter here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _ := Open(root)
	specs, problems := ws.LoadSpecs()
	if len(specs) != 1 {
		t.Errorf("got %d specs, want the one readable spec", len(specs))
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems, want 1", len(problems))
	}
}

func TestFindSpecIsCaseInsensitive(t *testing.T) {
	root := newWorkspace(t)
	writeSpec(t, root, feature.Backlog, "FTR-0001", "first", "")
	ws, _ := Open(root)
	for _, query := range []string{"FTR-0001", "ftr-0001", "  ftr-0001  "} {
		spec, err := ws.FindSpec(query)
		if err != nil {
			t.Fatalf("FindSpec(%q): %v", query, err)
		}
		if spec.ID != "FTR-0001" {
			t.Fatalf("FindSpec(%q) = %s", query, spec.ID)
		}
	}
	if _, err := ws.FindSpec("FTR-9999"); err == nil {
		t.Fatal("FindSpec for a missing feature should fail")
	}
}

func TestRel(t *testing.T) {
	root := newWorkspace(t)
	ws, _ := Open(root)
	got := ws.Rel(filepath.Join(ws.Root, Dir, "features", "backlog", "FTR-0001-x.md"))
	want := filepath.Join(Dir, "features", "backlog", "FTR-0001-x.md")
	if got != want {
		t.Fatalf("Rel = %q, want %q", got, want)
	}
	// A path outside the workspace comes back untouched rather than as a
	// misleading pile of `..` segments.
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	if got := ws.Rel(outside); got != outside {
		t.Fatalf("Rel(outside) = %q, want it unchanged", got)
	}
}

func samePath(t *testing.T, left, right string) bool {
	t.Helper()
	resolve := func(path string) string {
		if out, err := filepath.EvalSymlinks(path); err == nil {
			return out
		}
		return path
	}
	return resolve(left) == resolve(right)
}
