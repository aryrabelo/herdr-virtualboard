package herdrcli

import (
	"context"
	"fmt"
)

// Worktree is one entry of `herdr worktree list`.
type Worktree struct {
	Path            string `json:"path"`
	Branch          string `json:"branch"`
	Label           string `json:"label"`
	OpenWorkspaceID string `json:"open_workspace_id"`
	IsLinked        bool   `json:"is_linked_worktree"`
	IsBare          bool   `json:"is_bare"`
	IsDetached      bool   `json:"is_detached"`
	IsPrunable      bool   `json:"is_prunable"`
}

// WorktreeSource describes the repository a worktree list was taken from.
type WorktreeSource struct {
	RepoKey            string `json:"repo_key"`
	RepoName           string `json:"repo_name"`
	RepoRoot           string `json:"repo_root"`
	SourceCheckoutPath string `json:"source_checkout_path"`
	SourceWorkspaceID  string `json:"source_workspace_id"`
}

// WorktreeWorkspace is the workspace metadata Herdr attaches to a workspace
// that is a linked worktree checkout.
type WorktreeWorkspace struct {
	CheckoutPath string `json:"checkout_path"`
	IsLinked     bool   `json:"is_linked_worktree"`
	RepoKey      string `json:"repo_key"`
	RepoName     string `json:"repo_name"`
	RepoRoot     string `json:"repo_root"`
}

// CreatedWorktree is the result of `herdr worktree create`.
type CreatedWorktree struct {
	Workspace Workspace `json:"workspace"`
	Tab       Tab       `json:"tab"`
	RootPane  Pane      `json:"root_pane"`
	Worktree  Worktree  `json:"worktree"`
}

// Worktrees lists the worktrees of the repository containing cwd.
func (c *Client) Worktrees(ctx context.Context, cwd string) ([]Worktree, *WorktreeSource, error) {
	args := []string{"worktree", "list", "--cwd", cwd, "--trust-repository"}
	var result struct {
		Worktrees []Worktree     `json:"worktrees"`
		Source    WorktreeSource `json:"source"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, nil, err
	}
	return result.Worktrees, &result.Source, nil
}

// CreateWorktree cuts a branch and opens it as a linked Herdr workspace.
//
// This is Herdr's own worktree support rather than `git worktree add` plus a
// workspace: it produces a workspace the sidebar groups under the parent
// repository, which is exactly how a user expects a feature branch to appear.
//
// --trust-repository is required for worktree operations and is passed here
// because hvb only ever acts on a repository the user already opened as a
// VirtualBoard workspace.
func (c *Client) CreateWorktree(ctx context.Context, cwd, branch, base, label string, focus bool) (*CreatedWorktree, error) {
	args := []string{"worktree", "create", "--cwd", cwd, "--branch", branch}
	if base != "" {
		args = append(args, "--base", base)
	}
	if label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, focusFlag(focus), "--trust-repository")

	var result CreatedWorktree
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, err
	}
	if result.Workspace.WorkspaceID == "" {
		return nil, fmt.Errorf("herdr worktree create returned no workspace")
	}
	return &result, nil
}

// OpenWorktree opens an existing worktree as a workspace, by branch or path.
func (c *Client) OpenWorktree(ctx context.Context, cwd, branch, path, label string, focus bool) (*CreatedWorktree, error) {
	args := []string{"worktree", "open", "--cwd", cwd}
	switch {
	case path != "":
		args = append(args, "--path", path)
	case branch != "":
		args = append(args, "--branch", branch)
	default:
		return nil, fmt.Errorf("open worktree: a branch or a path is required")
	}
	if label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, focusFlag(focus), "--trust-repository")

	var result CreatedWorktree
	if err := c.call(ctx, &result, args...); err != nil {
		return nil, err
	}
	return &result, nil
}

// RemoveWorktree deletes a worktree checkout and closes its workspace.
//
// force discards uncommitted work, so callers must have confirmed with the
// user first: this is the one operation here that can destroy something an
// agent produced and nobody has reviewed.
func (c *Client) RemoveWorktree(ctx context.Context, workspaceID string, force bool) error {
	args := []string{"worktree", "remove", "--workspace", workspaceID}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--trust-repository")
	err := c.call(ctx, nil, args...)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

// WorkspaceMetadata reads a workspace, including its worktree metadata when it
// is a linked checkout.
func (c *Client) WorkspaceMetadata(ctx context.Context, workspaceID string) (*WorktreeWorkspace, error) {
	var result struct {
		Workspace struct {
			Worktree *WorktreeWorkspace `json:"worktree"`
		} `json:"workspace"`
	}
	if err := c.call(ctx, &result, "workspace", "get", workspaceID); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return result.Workspace.Worktree, nil
}
