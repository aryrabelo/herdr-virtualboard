package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/issuesrc"
)

// recordingRunner is the usina seam: it records the argv and answers with the
// payload usina prints, so the test never executes anything.
type recordingRunner struct {
	calls  [][]string
	stdout string
	err    error
}

func (r *recordingRunner) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
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
  "unidade": "ligar-o-board-na-linha",
  "issue": "321",
  "worktree": "/Users/ary/Sites/bora-team/worktrees/bugtoprompt/ligar-o-board-na-linha",
  "branch": "agente/ligar-o-board-na-linha",
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
		"--unidade", "ligar-o-board-na-linha",
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

	// The prompt file exists, is readable, and carries the card rather than a
	// briefing hvb invented. It is deliberately NOT deleted: usina cuts the
	// worktree and starts the pane before it prompts, so removing it here
	// would race the agent's own read.
	body, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("the prompt file usina was pointed at is unreadable: %v", err)
	}
	for _, fragment := range []string{
		"ligar o board na linha",
		"aryrabelo/bugtoprompt#321",
		"ceo-bora#321",
		"primeiro da fronteira",
	} {
		if !strings.Contains(string(body), fragment) {
			t.Errorf("the prompt never says %q:\n%s", fragment, body)
		}
	}

	if !strings.Contains(sentence, "wGZ:p1") || !strings.Contains(sentence, "agente/ligar-o-board-na-linha") {
		t.Errorf("the board shows %q, which names neither the pane to watch nor the branch to review", sentence)
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
