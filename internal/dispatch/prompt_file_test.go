package dispatch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
)

// fenceNonce matches the per-dispatch nonce, which is the only part of a
// prompt that differs between two builds of the same input. It appears in two
// spellings — quoted inside the markers, bare in the preamble that names it —
// and both have to be normalized or the comparison below is comparing nonces.
var fenceNonce = regexp.MustCompile(`(nonce[ =])"?[0-9a-f]+"?`)

func normalizeFenceNonce(prompt string) string {
	return fenceNonce.ReplaceAllString(prompt, `${1}NONCE`)
}

func promptFileInput(t *testing.T) PromptInput {
	t.Helper()
	defaults := config.Default()
	return PromptInput{
		Spec:    sampleSpec(t),
		Role:    roles.Role{Key: "backend_dev", Description: "Backend APIs"},
		Charter: "# Backend Developer\n\nYou implement APIs.",
		Column:  defaults.Column(feature.InProgress),
		RunID:   "ftr-0007-20260918t101500-a1b2c3d4",
		RelPath: ".virtualboard/features/in-progress/FTR-0007.md",
	}
}

// The file is the delivery channel now, so what it holds has to be the whole
// prompt — not a prefix, not a summary. The comparison is whole-string, on
// purpose: the defect this replaces was an agent receiving a line COUNT and no
// body, and a `strings.Contains` check would pass on a file holding the first
// paragraph and nothing else.
func TestWritePromptFileHoldsTheWholePromptVerbatim(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prompts")
	in := promptFileInput(t)

	path, err := WritePromptFile(dir, in)
	if err != nil {
		t.Fatalf("WritePromptFile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the written prompt: %v", err)
	}

	got, want := normalizeFenceNonce(string(raw)), normalizeFenceNonce(BuildPrompt(in))
	if got != want {
		t.Errorf("the file is not the prompt.\n--- file (%d bytes)\n%s\n--- BuildPrompt (%d bytes)\n%s",
			len(got), got, len(want), want)
	}

	// The fence travels with the text, so it must still be hvb's inside the
	// file: one block, closed by hvb, with the repository material in it.
	split := splitFence(t, string(raw))
	if !strings.Contains(split.inside, "Retry failed PUTs") {
		t.Errorf("the repository material is not inside the block in the file:\n%s", split.inside)
	}
	if !strings.Contains(split.before, "## Your contract") {
		t.Error("the contract is not above the block in the file")
	}

	// The prompt carries the role charter and the whole repository material,
	// so the file is kept as private as the run store that holds the same
	// material (internal/runs/store.go).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the written prompt: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("prompt file mode = %#o, want 0600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat the prompt directory hvb had to create: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("prompt directory mode = %#o, want 0700", perm)
	}
}

// A run id descends from the feature id, and on this board a feature id is a
// GitHub issue's: third-party text. Both doors are exercised — the run id hvb
// minted from it, and the spec id itself when there is no run id — because a
// name built from either one is a name built from someone else's string.
func TestWritePromptFileCannotBeTalkedOutOfItsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data", "prompts")

	hostile := []string{
		"../../../../etc/hvb-escape",
		"..",
		".",
		"aryrabelo/ceo-bora#160",
		`..\..\windows\escape`,
		strings.Repeat("../", 40) + "escape",
		"",
	}
	written := map[string]bool{}
	for _, id := range hostile {
		fromSpec := sampleSpec(t)
		fromSpec.ID = id
		for _, probe := range []struct {
			what string
			in   PromptInput
		}{
			{"run id", PromptInput{Spec: sampleSpec(t), Column: config.Column{}, RunID: id}},
			{"spec id", PromptInput{Spec: fromSpec, Column: config.Column{}}},
		} {
			path, err := WritePromptFile(dir, probe.in)
			if err != nil {
				t.Fatalf("WritePromptFile with hostile %s %q: %v", probe.what, id, err)
			}
			if parent := filepath.Dir(path); parent != dir {
				t.Errorf("hostile %s %q escaped: wrote to %s, want a file directly in %s",
					probe.what, id, path, dir)
			}
			if strings.Contains(filepath.Base(path), "..") {
				t.Errorf("hostile %s %q left %q in the file name", probe.what, id, filepath.Base(path))
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("hostile %s %q: %s is not the path the prompt landed on: %v",
					probe.what, id, path, err)
			}
			written[path] = true
		}
	}

	// Saying where the path points is not the same as saying nothing was
	// created elsewhere, so the tree above the directory is walked too.
	var strays []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !written[path] {
			strays = append(strays, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(strays) > 0 {
		t.Errorf("files appeared outside the paths WritePromptFile reported: %v", strays)
	}
}

// One file per run, keyed to the run id. Re-writing the same run replaces its
// own bytes, so a deferred resubmission does not pile up files; a different run
// gets a different file, so a second dispatch of the same card cannot take the
// prompt away from a live agent that is still reading it.
func TestWritePromptFileKeepsOneFilePerRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prompts")
	in := promptFileInput(t)

	first, err := WritePromptFile(dir, in)
	if err != nil {
		t.Fatalf("WritePromptFile: %v", err)
	}

	again, err := WritePromptFile(dir, in)
	if err != nil {
		t.Fatalf("resubmitting the same run: %v", err)
	}
	if again != first {
		t.Errorf("the same run wrote two files: %s and %s", first, again)
	}

	// Snapshotted after the resubmission, because replacing this run's own
	// bytes is the rule; what must not happen is ANOTHER run touching them.
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}

	second := in
	second.RunID = "ftr-0007-20260918t101500-99887766"
	other, err := WritePromptFile(dir, second)
	if err != nil {
		t.Fatalf("second run of the same card: %v", err)
	}
	if other == first {
		t.Fatalf("two runs of one card share %s, so the live run's prompt was overwritten", other)
	}
	// The first run is still live: its prompt must be exactly what it was.
	stillThere, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("the live run's prompt is gone: %v", err)
	}
	if string(stillThere) != string(firstBytes) {
		t.Errorf("a later dispatch changed the live run's prompt at %s", first)
	}

	// Two ids that reduce to the same file-name characters are still two
	// runs, and two runs never share a file.
	slugAlike := map[string]string{}
	for _, id := range []string{"ftr/0007-a1b2", "ftr-0007-a1b2", "ftr.0007.a1b2"} {
		run := in
		run.RunID = id
		path, err := WritePromptFile(dir, run)
		if err != nil {
			t.Fatalf("WritePromptFile(%q): %v", id, err)
		}
		if clash, ok := slugAlike[path]; ok {
			t.Errorf("runs %q and %q share the file %s", clash, id, path)
		}
		slugAlike[path] = id
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Two runs of the card, three slug-alike ids, and no temporary file left
	// behind by the atomic write.
	if len(entries) != 5 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("%s holds %d files, want 5: %v", dir, len(entries), names)
	}
}

// A dispatch that runs without its prompt is worse than one that refuses: the
// agent would sit in a pane with no task and the board would call it started.
// So the failure is returned, and it names the path it tried, because the
// operator reading it has to know which directory to fix.
func TestWritePromptFileRefusesAndNamesThePathItTried(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "prompts")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "inside")

	path, err := WritePromptFile(dir, promptFileInput(t))
	if err == nil {
		t.Fatalf("writing under a plain file succeeded, returning %s", path)
	}
	if path != "" {
		t.Errorf("a failed write returned the path %q, which nothing wrote", path)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the error does not name the path it tried (%s): %v", dir, err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("the error does not carry why it failed: %v", err)
	}
}

// This is the text that actually travels over the terminal, so it is held to
// being small, being unambiguous about which file to read, and carrying none
// of the content it points at.
func TestFilePointerPromptPointsWithoutRepeating(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prompts")
	in := promptFileInput(t)
	path, err := WritePromptFile(dir, in)
	if err != nil {
		t.Fatalf("WritePromptFile: %v", err)
	}

	pointer := FilePointerPrompt(path)

	if !strings.Contains(pointer, path) {
		t.Errorf("the pointer does not name the file it points at (%s):\n%s", path, pointer)
	}
	if len(pointer) > promptPointerBudget {
		t.Errorf("the pointer is %d bytes, over the %d-byte budget that keeps it out of a "+
			"collapsed paste:\n%s", len(pointer), promptPointerBudget, pointer)
	}
	// The whole point is that the body does NOT travel this way.
	prompt := BuildPrompt(in)
	if len(pointer)*5 > len(prompt) {
		t.Errorf("the pointer is %d bytes against a %d-byte prompt, which is not a pointer",
			len(pointer), len(prompt))
	}
	for _, leaked := range []string{
		"## Your contract",
		"untrusted-content",
		"Retry failed PUTs",
		"You implement APIs.",
		"#### Role charter",
	} {
		if strings.Contains(pointer, leaked) {
			t.Errorf("the pointer repeats %q from the prompt body:\n%s", leaked, pointer)
		}
	}
	// If the agent never opens the file, the reporting contract still has to
	// have reached it, or the run hangs on the board.
	if !strings.Contains(pointer, "hvb run done") {
		t.Errorf("the pointer does not carry the reporting contract:\n%s", pointer)
	}
	// Nothing harness-specific: claude, omp and codex all just read prose.
	for _, harnessSyntax := range []string{"@", "/read", "local://", "attachment://"} {
		if strings.Contains(pointer, harnessSyntax) {
			t.Errorf("the pointer uses %q, which is one harness's syntax:\n%s", harnessSyntax, pointer)
		}
	}
	// The file it names must be the one that exists.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the pointer names %s, which is not there: %v", path, err)
	}
}
