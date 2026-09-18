package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	// The working directory is not a field: the injected runner already
	// carries it (internal/cli/queue.go wraps issuesrc.DirRunner), and a
	// second copy here could only ever disagree with the directory the child
	// actually runs in.
}

// DispatchIssue writes the card to a prompt file and dispatches it.
//
// The prompt has to be a file: `usina agente dispatch` refuses an inline spec
// ("--prompt-file é obrigatório fora do dry-run (spec em arquivo, nunca
// inline)", measured 2026-09-17), and the refusal is a good rule rather than an
// obstacle — a spec that reached the agent through an argv would be truncated
// by the first shell in the chain that disagreed about quoting.
//
// The staging directory is removed on the way out, and that it cannot race the
// agent is measured rather than assumed: the usina reads the whole file into
// memory BEFORE it cuts the worktree, sends that text — never the path — to
// `bora agent prompt`, and waits for the pane to consume it before printing the
// JSON this call parses (usina/verbos/agente.py: ler_texto, then _worktree,
// _prompt, _esperar_consumo). Nothing holds the path once the child has exited,
// so leaving the directory behind would only leak one temp dir per dispatch.
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
	// Deferred rather than repeated on each return: the write can fail, the
	// usina can fail, and both paths owe the same cleanup.
	defer os.RemoveAll(dir)

	promptFile := filepath.Join(dir, fmt.Sprintf("issue-%d.md", number))
	if err := os.WriteFile(promptFile, []byte(issuePrompt(spec, number)), 0o600); err != nil {
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
//
// The execution repository is deliberately absent: it is where the work is
// branched from, which the usina already knows from --repo and the agent
// already stands in. What the agent needs named is the issue to READ.
func issuePrompt(spec *feature.Spec, number int) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", strings.TrimSpace(spec.Title))
	fmt.Fprintf(&out, "Issue: %s\n", issueReference(spec, number))
	fmt.Fprintf(&out, "Card: %s\n", spec.ID)
	if body := strings.TrimSpace(spec.Body); body != "" {
		fmt.Fprintf(&out, "\n%s\n", body)
	}
	return out.String()
}

// issueReference names the issue the card came from, never the repository the
// work happens in.
//
// The two differ by design — the cards come from a CEO repository while
// --dispatch-repo points at the execution one — so spelling
// `<execution repo>#<number>` sent the agent to read whatever issue happened to
// carry that number where it was branching from: a different issue, or none at
// all. The card's own URL is the honest source, and internal/issuesrc puts it
// in Path precisely because an issue does not live on disk.
func issueReference(spec *feature.Spec, number int) string {
	if repo, ok := repoOfIssueURL(spec.Path); ok {
		return fmt.Sprintf("%s#%d", repo, number)
	}
	// No URL to read owner/name out of: the URL itself, and failing that the
	// bare number, is narrower than `owner/name#N` but never wrong. The body
	// still carries whatever the source knew.
	if path := strings.TrimSpace(spec.Path); path != "" {
		return path
	}
	return fmt.Sprintf("#%d", number)
}

// repoOfIssueURL reads owner/name out of a forge issue URL
// (https://host/owner/name/issues/N, which is GitHub's shape and Forgejo's).
//
// The `issues` segment is required rather than ignored: without it any URL a
// source happened to put in Path would mint an owner/name pair the prompt
// states as fact, and a wrong repository stated confidently is worse than the
// URL printed as it stands.
func repoOfIssueURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 4 || segments[0] == "" || segments[1] == "" || segments[2] != "issues" {
		return "", false
	}
	return segments[0] + "/" + segments[1], true
}

// issueNumberOf reads the issue number off the card's own label rather than off
// its id: an id is an opaque string a source may mint from a content hash, and
// the label is the contract every source shares.
//
// The whole suffix has to parse, which is why this is strconv.Atoi and not
// fmt.Sscanf: Sscanf accepts a numeric PREFIX, so a label spelled
// `hvb:issue:321-extra` dispatched issue 321 and cut a worktree for a number
// nobody wrote. A label hvb cannot read names no issue.
func issueNumberOf(spec *feature.Spec) int {
	if spec == nil {
		return 0
	}
	for _, label := range spec.Labels {
		rest, ok := strings.CutPrefix(strings.TrimSpace(label), issuesrc.LabelIssuePrefix)
		if !ok {
			continue
		}
		number, err := strconv.Atoi(rest)
		if err != nil || number <= 0 {
			continue
		}
		return number
	}
	return 0
}
