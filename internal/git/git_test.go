package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepo builds a real git repository in a temp dir. These tests use real git
// rather than a stub: the point of this package is that its assumptions about
// git's porcelain hold, and a stub would only assert that hvb calls what hvb
// expects to call.
func newRepo(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	write(t, filepath.Join(dir, "README.md"), "hello\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-qm", "initial")

	repo, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return repo
}

// withBareRemote gives the repo an `origin` that is a bare repository on disk,
// so push is exercised for real with no network involved.
func withBareRemote(t *testing.T, repo *Repo) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	run(t, t.TempDir(), "init", "-q", "--bare", bare)
	run(t, repo.Root, "remote", "add", "origin", bare)
	run(t, repo.Root, "push", "-q", "--set-upstream", "origin", "main")
	return bare
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenResolvesTheTopLevel(t *testing.T) {
	repo := newRepo(t)
	nested := filepath.Join(repo.Root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := Open(context.Background(), nested)
	if err != nil {
		t.Fatal(err)
	}
	if resolve(t, found.Root) != resolve(t, repo.Root) {
		t.Fatalf("Root = %q, want %q", found.Root, repo.Root)
	}
}

func TestOpenRejectsANonRepository(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Open outside a repository should fail")
	}
}

func TestDefaultBranchFallsBackToTheLocalBranch(t *testing.T) {
	repo := newRepo(t)
	branch, err := repo.DefaultBranch(context.Background(), "origin")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Fatalf("DefaultBranch = %q, want main", branch)
	}
}

func TestCleanReportsUntrackedFiles(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	clean, err := repo.Clean(ctx)
	if err != nil || !clean {
		t.Fatalf("a fresh checkout should be clean (got %v, %v)", clean, err)
	}

	// An untracked file counts: an agent that wrote a new file and never
	// added it has not finished, and a pull request would silently omit it.
	write(t, filepath.Join(repo.Root, "new.txt"), "x\n")
	clean, err = repo.Clean(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Fatal("an untracked file must count as unclean")
	}
}

func TestCommitsAhead(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	run(t, repo.Root, "checkout", "-q", "-b", "feature/FTR-0001/thing")

	ahead, err := repo.CommitsAhead(ctx, "feature/FTR-0001/thing", "main")
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 0 {
		t.Fatalf("a fresh branch is %d commits ahead, want 0", ahead)
	}

	write(t, filepath.Join(repo.Root, "a.txt"), "a\n")
	run(t, repo.Root, "add", "-A")
	run(t, repo.Root, "commit", "-qm", "add a")
	write(t, filepath.Join(repo.Root, "b.txt"), "b\n")
	run(t, repo.Root, "add", "-A")
	run(t, repo.Root, "commit", "-qm", "add b")

	ahead, err = repo.CommitsAhead(ctx, "feature/FTR-0001/thing", "main")
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 2 {
		t.Fatalf("CommitsAhead = %d, want 2", ahead)
	}
}

func TestLogReturnsSubjectsNewestFirst(t *testing.T) {
	repo := newRepo(t)
	run(t, repo.Root, "checkout", "-q", "-b", "topic")
	for _, subject := range []string{"first change", "second change"} {
		write(t, filepath.Join(repo.Root, subject+".txt"), "x\n")
		run(t, repo.Root, "add", "-A")
		run(t, repo.Root, "commit", "-qm", subject)
	}
	subjects, err := repo.Log(context.Background(), "topic", "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 2 || subjects[0] != "second change" {
		t.Fatalf("Log = %v, want newest first", subjects)
	}
}

// Push is exercised against a bare repository on disk, so the whole path runs
// with no network and no credentials.
func TestPushToALocalBareRemote(t *testing.T) {
	repo := newRepo(t)
	bare := withBareRemote(t, repo)
	ctx := context.Background()

	run(t, repo.Root, "checkout", "-q", "-b", "feature/FTR-0007/retry")
	write(t, filepath.Join(repo.Root, "retry.go"), "package main\n")
	run(t, repo.Root, "add", "-A")
	run(t, repo.Root, "commit", "-qm", "FTR-0007: add retry")

	if err := repo.Push(ctx, "origin", "feature/FTR-0007/retry"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	out := run(t, bare, "branch", "--list", "feature/FTR-0007/retry")
	if out == "" {
		t.Fatal("the branch did not reach the remote")
	}
}

func TestRemoteURL(t *testing.T) {
	repo := newRepo(t)
	bare := withBareRemote(t, repo)
	url, err := repo.RemoteURL(context.Background(), "origin")
	if err != nil {
		t.Fatal(err)
	}
	if resolve(t, url) != resolve(t, bare) {
		t.Fatalf("RemoteURL = %q, want %q", url, bare)
	}
	if _, err := repo.RemoteURL(context.Background(), "upstream"); err == nil {
		t.Fatal("a missing remote should be an error")
	}
}

// A dispatch running in the background must fail with a message rather than
// block forever on an invisible credential prompt.
func TestPushDoesNotPromptForCredentials(t *testing.T) {
	repo := newRepo(t)
	run(t, repo.Root, "remote", "add", "origin", "https://127.0.0.1:1/nope.git")
	run(t, repo.Root, "checkout", "-q", "-b", "topic")
	err := repo.Push(context.Background(), "origin", "topic")
	if err == nil {
		t.Fatal("pushing to an unreachable remote should fail, not hang")
	}
}

func resolve(t *testing.T, path string) string {
	t.Helper()
	if out, err := filepath.EvalSymlinks(path); err == nil {
		return out
	}
	return path
}
