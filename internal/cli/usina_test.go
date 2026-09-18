package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/issuesrc"
)

// recordingRunner is the usina seam: it records the argv and answers with the
// payload usina prints, so the test never executes anything.
//
// It also reads the --prompt-file while it is being "run", which is what the
// real usina does (measured: it reads the spec into memory before it cuts the
// worktree) and the only moment the file is guaranteed to exist — hvb owns the
// staging directory and removes it on the way out.
type recordingRunner struct {
	calls   [][]string
	prompts []string
	stdout  string
	err     error
}

func (r *recordingRunner) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	for index, arg := range args {
		if arg != "--prompt-file" || index+1 >= len(args) {
			continue
		}
		body, err := os.ReadFile(args[index+1])
		if err != nil {
			return nil, fmt.Errorf("the prompt file usina was pointed at is unreadable: %w", err)
		}
		r.prompts = append(r.prompts, string(body))
	}
	if r.err != nil {
		return nil, r.err
	}
	return []byte(r.stdout), nil
}

// The measured payload of `usina agente dispatch`, trimmed to the keys hvb
// reads. `issue` is a string there, not a number — measured from the
// .usina-despacho.json this worktree was dispatched with.
const usinaDispatchPayload = `{
  "verbo": "agente",
  "repo": "aryrabelo/bugtoprompt",
  "unidade": "ligar-o-board-na-linha-321",
  "issue": "321",
  "worktree": "/Users/ary/Sites/bora-team/worktrees/bugtoprompt/ligar-o-board-na-linha-321",
  "branch": "agente/ligar-o-board-na-linha-321",
  "workspace": "wGZ",
  "pane": "wGZ:p1",
  "outcome": "consumed"
}`

func issueSpec() *feature.Spec {
	return &feature.Spec{
		Frontmatter: feature.Frontmatter{
			ID:    "ceo-bora#321",
			Title: "ligar o board na linha",
			Labels: []string{
				issuesrc.LabelSourceIssue,
				issuesrc.LabelIssuePrefix + "321",
			},
		},
		Path: "https://github.com/aryrabelo/ceo-bora/issues/321",
		Body: "https://github.com/aryrabelo/ceo-bora/issues/321\n\nprimeiro da fronteira",
	}
}

// `d` on an issue card reaches `usina agente dispatch` with the execution
// repository, the slug and the issue number, and with the spec in a FILE —
// usina refuses an inline spec, and an argv would be truncated by the first
// shell in the chain that disagreed about quoting.
func TestDispatchIssueCallsUsinaWithThePromptInAFile(t *testing.T) {
	runner := &recordingRunner{stdout: usinaDispatchPayload}
	despatcher := &usinaDespatcher{run: runner.run, repo: "aryrabelo/bugtoprompt"}

	sentence, err := despatcher.DispatchIssue(context.Background(), issueSpec())
	if err != nil {
		t.Fatalf("dispatching an issue card: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("usina was called %d times, want once: %v", len(runner.calls), runner.calls)
	}

	// calls[0][0] is the binary the runner was asked for, so the flags start
	// at index 1 and the argv is eleven elements long.
	argv := runner.calls[0]
	if len(argv) != 11 {
		t.Fatalf("argv has %d elements: %v", len(argv), argv)
	}
	promptFile := argv[10]
	want := []string{
		"usina", "agente", "dispatch",
		"--repo", "aryrabelo/bugtoprompt",
		"--unidade", "ligar-o-board-na-linha-321",
		"--issue", "321",
		"--prompt-file", promptFile,
	}
	// The flag name and its value are one element each, so the comparison is
	// element by element rather than on a joined string: a joined one passes
	// when two flags swap places.
	for index, expected := range want {
		if argv[index] != expected {
			t.Errorf("argv[%d] is %q, want %q (full: %v)", index, argv[index], expected, argv)
		}
	}

	// The file was readable while the usina was running — the runner read it
	// there, exactly as the usina does — and carries the card rather than a
	// briefing hvb invented.
	if len(runner.prompts) != 1 {
		t.Fatalf("the runner read %d prompt files, want one", len(runner.prompts))
	}
	body := runner.prompts[0]
	for _, fragment := range []string{
		"ligar o board na linha",
		"Issue: aryrabelo/ceo-bora#321",
		"ceo-bora#321",
		"primeiro da fronteira",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("the prompt never says %q:\n%s", fragment, body)
		}
	}

	if !strings.Contains(sentence, "wGZ:p1") || !strings.Contains(sentence, "agente/ligar-o-board-na-linha-321") {
		t.Errorf("the board shows %q, which names neither the pane to watch nor the branch to review", sentence)
	}
}

// The staging directory belongs to hvb and does not outlive the dispatch. The
// usina reads the spec into memory before it cuts the worktree and waits for
// the pane to consume it before exiting (measured in usina/verbos/agente.py),
// so nothing holds the path once the call returned — leaving it behind leaked
// one temp directory per press of `d`.
func TestDispatchIssueRemovesTheStagedPromptDirectory(t *testing.T) {
	runner := &recordingRunner{stdout: usinaDispatchPayload}
	despatcher := &usinaDespatcher{run: runner.run, repo: "aryrabelo/bugtoprompt"}

	if _, err := despatcher.DispatchIssue(context.Background(), issueSpec()); err != nil {
		t.Fatalf("dispatching an issue card: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("usina was called %d times, want once: %v", len(runner.calls), runner.calls)
	}
	promptFile := runner.calls[0][len(runner.calls[0])-1]

	if _, err := os.Stat(promptFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staged prompt %s survived the dispatch (stat: %v)", promptFile, err)
	}
	if dir := filepath.Dir(promptFile); !strings.Contains(dir, "hvb-dispatch-") {
		t.Errorf("the prompt was staged in %s, which is not a directory hvb owns and may delete", dir)
	} else if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging directory %s survived the dispatch (stat: %v)", dir, err)
	}

	// A usina that refused owes the same cleanup: the prompt is staged before
	// the child runs, so the failing path is the one that leaks twice as fast.
	failing := &recordingRunner{stdout: usinaDispatchPayload, err: errors.New("instancia da usina indeterminada")}
	failed := &usinaDespatcher{run: failing.run, repo: "aryrabelo/bugtoprompt"}
	if _, err := failed.DispatchIssue(context.Background(), issueSpec()); err == nil {
		t.Fatal("a usina that refused was reported as a dispatch")
	}
	if len(failing.calls) != 1 {
		t.Fatalf("usina was called %d times, want once: %v", len(failing.calls), failing.calls)
	}
	refusedDir := filepath.Dir(failing.calls[0][len(failing.calls[0])-1])
	if _, err := os.Stat(refusedDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging directory %s survived a refused dispatch (stat: %v)", refusedDir, err)
	}
}

// The prompt points the agent at the issue the CARD came from. The queue and
// the execution repository are different repositories by design, so naming the
// execution one sent the agent to read whatever issue happened to carry that
// number there — a different issue, or none.
func TestDispatchIssuePromptNamesTheCardsIssueNotTheExecutionRepository(t *testing.T) {
	runner := &recordingRunner{stdout: usinaDispatchPayload}
	despatcher := &usinaDespatcher{run: runner.run, repo: "aryrabelo/bugtoprompt"}

	if _, err := despatcher.DispatchIssue(context.Background(), issueSpec()); err != nil {
		t.Fatalf("dispatching an issue card: %v", err)
	}
	if len(runner.prompts) != 1 {
		t.Fatalf("the runner read %d prompt files, want one", len(runner.prompts))
	}
	body := runner.prompts[0]

	if !strings.Contains(body, "Issue: aryrabelo/ceo-bora#321") {
		t.Errorf("the prompt does not name the card's own issue:\n%s", body)
	}
	// The execution repository still travels, but only as --repo: it is where
	// the work is branched from, not an issue to read.
	if strings.Contains(body, "aryrabelo/bugtoprompt") {
		t.Errorf("the prompt names the execution repository, whose issue 321 is a different issue:\n%s", body)
	}
	if argv := runner.calls[0]; argv[3] != "--repo" || argv[4] != "aryrabelo/bugtoprompt" {
		t.Errorf("--repo no longer carries the execution repository: %v", argv)
	}

	// A card whose source URL says nothing falls back to the number rather
	// than to a repository nobody named.
	card := issueSpec()
	card.Path = ""
	if prompt := issuePrompt(card, 321); !strings.Contains(prompt, "Issue: #321\n") {
		t.Errorf("a card with no URL names its issue as:\n%s", prompt)
	}
	// A URL that is not an issue URL is printed as it stands: a wrong
	// owner/name stated as fact is worse than a URL the reader can follow.
	card.Path = "https://github.com/aryrabelo/ceo-bora"
	if prompt := issuePrompt(card, 321); !strings.Contains(prompt, "Issue: https://github.com/aryrabelo/ceo-bora\n") {
		t.Errorf("a non-issue URL was reshaped into a repository reference:\n%s", prompt)
	}
}

// A card with no issue label names no issue, and dispatching one would cut a
// worktree for nothing. It is refused before any child process runs.
func TestDispatchIssueRefusesACardWithNoIssueNumber(t *testing.T) {
	runner := &recordingRunner{stdout: usinaDispatchPayload}
	despatcher := &usinaDespatcher{run: runner.run, repo: "aryrabelo/bugtoprompt"}

	card := issueSpec()
	card.Labels = []string{issuesrc.LabelSourceIssue}

	if _, err := despatcher.DispatchIssue(context.Background(), card); err == nil {
		t.Fatal("a card with no issue label was dispatched")
	} else if !strings.Contains(err.Error(), issuesrc.LabelIssuePrefix) {
		t.Errorf("the refusal does not name the label it needed: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("usina was called %d times for a card with no issue", len(runner.calls))
	}
}

// The number comes off the card's own label, never off its id: an id is an
// opaque string a source may mint from a content hash, and internal/ghboard
// spells the same prefix for the issue a pull request CLOSES.
func TestIssueNumberComesFromTheLabelNotTheID(t *testing.T) {
	card := issueSpec()
	card.ID = "d41d8cd98f00b204"
	if got := issueNumberOf(card); got != 321 {
		t.Errorf("got %d from a hashed id, want 321 read off the label", got)
	}

	card.Labels = []string{issuesrc.LabelSourceIssue, issuesrc.LabelIssuePrefix + "aryrabelo/bugtoprompt#31"}
	if got := issueNumberOf(card); got != 0 {
		t.Errorf("a cross-repository reference parsed as %d; it names another repository's issue", got)
	}

	if got := issueNumberOf(nil); got != 0 {
		t.Errorf("no card answered %d", got)
	}
}

// A label whose suffix is not exactly a number names no issue. The whole
// suffix has to parse: a numeric PREFIX used to be enough, so
// `hvb:issue:321-extra` cut a worktree for issue 321 — a number nobody wrote,
// on a card whose label hvb could not read.
func TestIssueNumberRejectsALabelThatIsNotExactlyANumber(t *testing.T) {
	for _, suffix := range []string{
		"321-extra",
		"321 extra",
		"321px",
		"321.5",
		"0",
		"-321",
		"",
		"   ",
		"0x141",
	} {
		card := issueSpec()
		card.Labels = []string{issuesrc.LabelSourceIssue, issuesrc.LabelIssuePrefix + suffix}
		if got := issueNumberOf(card); got != 0 {
			t.Errorf("%q parsed as issue %d; only an exact number names an issue",
				issuesrc.LabelIssuePrefix+suffix, got)
		}
	}

	// And the consequence: nothing is dispatched, so no worktree is cut for a
	// label hvb could not read.
	runner := &recordingRunner{stdout: usinaDispatchPayload}
	despatcher := &usinaDespatcher{run: runner.run, repo: "aryrabelo/bugtoprompt"}
	card := issueSpec()
	card.Labels = []string{issuesrc.LabelSourceIssue, issuesrc.LabelIssuePrefix + "321-extra"}

	if _, err := despatcher.DispatchIssue(context.Background(), card); err == nil {
		t.Fatal("a card labelled hvb:issue:321-extra was dispatched")
	} else if !strings.Contains(err.Error(), issuesrc.LabelIssuePrefix) {
		t.Errorf("the refusal does not name the label it needed: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("usina was called %d times for a malformed label: %v", len(runner.calls), runner.calls)
	}
}
