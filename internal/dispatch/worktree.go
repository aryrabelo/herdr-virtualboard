package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/netors/herdr-virtualboard/internal/feature"
	"github.com/netors/herdr-virtualboard/internal/git"
	"github.com/netors/herdr-virtualboard/internal/runs"
)

// ErrNotAGitRepo reports that a worktree run was asked for in a directory that
// is not a git repository.
var ErrNotAGitRepo = errors.New("worktree dispatch needs a git repository")

// prepareWorktree cuts the feature's branch and opens it as a linked Herdr
// workspace, returning what the run should record.
//
// An existing worktree for the same branch is reused rather than treated as an
// error: a second dispatch of a feature is a retry, and it should land in the
// same place as the first so the work accumulates on one branch.
func (d *Dispatcher) prepareWorktree(ctx context.Context, spec *feature.Spec) (*runs.Worktree, error) {
	repo, err := git.Open(ctx, d.Workspace.Root)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotAGitRepo, d.Workspace.Root)
	}
	if d.GitBin != "" {
		repo.Bin = d.GitBin
	}

	settings := d.Config.Worktree
	remote := settings.Remote
	if remote == "" {
		remote = "origin"
	}

	base := settings.Base
	if base == "" {
		base, err = repo.DefaultBranch(ctx, remote)
		if err != nil {
			return nil, fmt.Errorf("resolve the branch to cut from: %w", err)
		}
	}
	branch := git.BranchName(settings.Branch, spec.ID, spec.Title)

	// Reuse an existing checkout for this branch before creating one.
	existing, _, err := d.Herdr.Worktrees(ctx, repo.Root)
	if err == nil {
		for _, candidate := range existing {
			if candidate.Branch != branch || !candidate.IsLinked {
				continue
			}
			opened, err := d.Herdr.OpenWorktree(ctx, repo.Root, branch, candidate.Path,
				runs.TabLabel(spec.ID), false)
			if err != nil {
				return nil, fmt.Errorf("reopen the existing worktree for %s: %w", branch, err)
			}
			return &runs.Worktree{
				Path: candidate.Path, Branch: branch, Base: base, Remote: remote,
				WorkspaceID: opened.Workspace.WorkspaceID, RepoRoot: repo.Root,
			}, nil
		}
	}

	created, err := d.Herdr.CreateWorktree(ctx, repo.Root, branch, base, runs.TabLabel(spec.ID), false)
	if err != nil {
		return nil, fmt.Errorf("create the worktree for %s: %w", branch, err)
	}
	// Herdr reports the checkout path in three places; take whichever is
	// populated rather than depending on one field of one object.
	path := created.Worktree.Path
	if path == "" && created.Workspace.Worktree != nil {
		path = created.Workspace.Worktree.CheckoutPath
	}
	if path == "" {
		path = created.RootPane.CWD
	}
	if path == "" {
		return nil, fmt.Errorf("herdr created the worktree for %s but reported no checkout path", branch)
	}
	return &runs.Worktree{
		Path: path, Branch: branch, Base: base, Remote: remote,
		WorkspaceID: created.Workspace.WorkspaceID, RepoRoot: repo.Root,
	}, nil
}

// repoFor returns a git.Repo rooted at the run's working directory: the
// worktree checkout when there is one, otherwise the project root.
func (d *Dispatcher) repoFor(run *runs.Run) *git.Repo {
	root := d.Workspace.Root
	if run.Worktree != nil && run.Worktree.Path != "" {
		root = run.Worktree.Path
	}
	return &git.Repo{Root: root, Bin: d.GitBin}
}

// worktreePromptSection is appended to the dispatch prompt for a worktree run.
//
// Everything here exists because an agent that does not know it is on a branch
// behaves badly in a specific way: it leaves work uncommitted, and hvb then has
// nothing to open a pull request from.
func worktreePromptSection(wt *runs.Worktree, wantPR bool) string {
	var b strings.Builder
	b.WriteString("## Where you are working\n\n")
	fmt.Fprintf(&b, "You are in an isolated git worktree, not the main checkout:\n\n")
	fmt.Fprintf(&b, "- **directory**: `%s` (already your working directory)\n", wt.Path)
	fmt.Fprintf(&b, "- **branch**: `%s`, cut from `%s`\n", wt.Branch, wt.Base)
	fmt.Fprintf(&b, "- **repository**: `%s`\n\n", wt.RepoRoot)
	b.WriteString("This means:\n\n")
	b.WriteString("1. **Commit your work.** Work left uncommitted is invisible to everything ")
	b.WriteString("downstream and will be lost when the worktree is cleaned up. Commit in ")
	b.WriteString("logical steps with messages that say why, and reference the feature id.\n")
	b.WriteString("2. **Stay on this branch.** Do not switch branches, rebase onto anything, ")
	b.WriteString("or touch the main checkout.\n")
	if wantPR {
		b.WriteString("3. **Do not push and do not open a pull request.** hvb pushes the branch ")
		b.WriteString("and opens the pull request itself once you report success, so that the ")
		b.WriteString("board, the branch and the feature spec stay consistent.\n")
	} else {
		b.WriteString("3. **Do not push.** Leave the branch local; a human decides what happens to it.\n")
	}
	b.WriteString("\n")
	return b.String()
}
