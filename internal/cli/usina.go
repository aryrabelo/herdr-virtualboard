package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/issuesrc"
	"github.com/virtualboard/herdr-virtualboard/internal/usinasrc"
)

// usinaDespatcher hands a GitHub issue card to `usina agente dispatch`.
//
// It lives in internal/cli, not in internal/tui, and that is the same seam the
// rest of this package already is: the CLI is the only layer that knows how to
// reach an external binary, and the board only holds an interface
// (tui.IssueDispatcher) plus the sentence to print. internal/tui therefore
// keeps no dependency on the usina, the process runner, or a temporary file.
type usinaDespatcher struct {
	// run is the usina seam; tests swap it for a recorder and never execute
	// anything.
	run usinasrc.Runner
	// repo is what `usina agente dispatch --repo` receives: the EXECUTION
	// repository, from which the usina resolves the canonical checkout it
	// cuts a worktree out of.
	//
	// It is not the issue's own repository, and that is the whole reason it
	// is a separate flag. The cards come from a CEO repository
	// (`--issues aryrabelo/ceo-bora`) while the work happens in
	// `aryrabelo/bugtoprompt`, so deriving one from the other would dispatch
	// an agent into the planning repository.
	repo string
	// dir is the working directory the child runs in. The usina resolves
	// WHICH instance it is talking about from the directory it runs in and
	// refuses outright from anywhere else (measured: "instancia da usina
	// indeterminada: nenhum segmento"), so this is not a convenience.
	dir string
}

// DispatchIssue writes the card to a prompt file and dispatches it.
//
// The prompt has to be a file: `usina agente dispatch` refuses an inline spec
// ("--prompt-file é obrigatório fora do dry-run (spec em arquivo, nunca
// inline)", measured 2026-09-17), and the refusal is a good rule rather than an
// obstacle — a spec that reached the agent through an argv would be truncated
// by the first shell in the chain that disagreed about quoting.
//
// The file is left in place, deliberately. The usina reads it asynchronously:
// it cuts the worktree, starts the pane and only then prompts, so deleting the
// file when this function returns would race the agent's own read. The
// directory is the OS temporary one, which the OS reaps.
func (d *usinaDespatcher) DispatchIssue(ctx context.Context, spec *feature.Spec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	number := issueNumberOf(spec)
	if number <= 0 {
		return "", fmt.Errorf("%s carries no %s label, so nothing names the issue to dispatch",
			spec.ID, issuesrc.LabelIssuePrefix)
	}

	dir, err := os.MkdirTemp("", "hvb-dispatch-")
	if err != nil {
		return "", fmt.Errorf("stage the dispatch prompt: %w", err)
	}
	promptFile := filepath.Join(dir, fmt.Sprintf("issue-%d.md", number))
	if err := os.WriteFile(promptFile, []byte(issuePrompt(spec, d.repo, number)), 0o600); err != nil {
		return "", fmt.Errorf("write the dispatch prompt: %w", err)
	}

	result, err := usinasrc.Dispatch(d.run, usinasrc.DispatchRequest{
		Repo:       d.repo,
		Unidade:    usinasrc.Unidade(spec.Title, number),
		Issue:      number,
		PromptFile: promptFile,
	})
	if err != nil {
		return "", err
	}
	// The sentence names the pane and the branch because those are the two
	// things the reader acts on next: one to watch the agent, one to review
	// what it wrote.
	return fmt.Sprintf("usina dispatched %s to %s on %s", spec.ID, result.Pane, result.Branch), nil
}

// issuePrompt is the spec the dispatched agent reads.
//
// It is the card, not a briefing hvb invents: the title, the issue to read, and
// the body the source already assembled (the URL, and the frontier's own
// sentence about why this issue is next). The usina wraps it with its own
// session lock, so nothing about panes, channels or worktrees belongs here.
func issuePrompt(spec *feature.Spec, repo string, number int) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", strings.TrimSpace(spec.Title))
	fmt.Fprintf(&out, "Issue: %s#%d\n", repo, number)
	fmt.Fprintf(&out, "Card: %s\n", spec.ID)
	if body := strings.TrimSpace(spec.Body); body != "" {
		fmt.Fprintf(&out, "\n%s\n", body)
	}
	return out.String()
}

// issueNumberOf reads the issue number off the card's own label rather than off
// its id: an id is an opaque string a source may mint from a content hash, and
// the label is the contract every source shares.
func issueNumberOf(spec *feature.Spec) int {
	if spec == nil {
		return 0
	}
	for _, label := range spec.Labels {
		rest, ok := strings.CutPrefix(strings.TrimSpace(label), issuesrc.LabelIssuePrefix)
		if !ok {
			continue
		}
		number := 0
		if _, err := fmt.Sscanf(rest, "%d", &number); err != nil {
			continue
		}
		return number
	}
	return 0
}
