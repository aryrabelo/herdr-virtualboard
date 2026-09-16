package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// githubCLI opens pull requests through `gh`.
//
// Using the CLI rather than the REST API is deliberate: `gh` already holds the
// user's credentials, in their keyring, refreshed and scoped. Asking them to
// mint a second token for hvb to store would be worse in every way.
type githubCLI struct{ bin string }

func (g *githubCLI) Name() string { return "gh" }

func (g *githubCLI) binary() string {
	if g.bin != "" {
		return g.bin
	}
	return "gh"
}

func (g *githubCLI) Open(ctx context.Context, req Request) (*Result, error) {
	if _, err := exec.LookPath(g.binary()); err != nil {
		return &Result{Reason: "the GitHub CLI (gh) is not installed — open the pull request from the compare page"}, nil
	}
	if reason := g.authReason(ctx); reason != "" {
		return &Result{Reason: reason}, nil
	}

	args := []string{
		"pr", "create",
		"--repo", req.Remote.Slug(),
		"--base", req.Base,
		"--head", req.Head,
		"--title", req.Title,
		"--body", req.Body,
	}
	if req.Draft {
		args = append(args, "--draft")
	}

	out, err := g.run(ctx, 2*time.Minute, args...)
	if err != nil {
		// gh reports an existing pull request as a failure; that is a
		// success for hvb's purposes, so recover its URL rather than
		// telling the user something went wrong.
		if existing := g.existingPR(ctx, req); existing != nil {
			return existing, nil
		}
		return nil, err
	}
	url := lastURL(out)
	return &Result{Opened: true, URL: url, Number: numberFromURL(url)}, nil
}

// authReason returns a user-facing explanation when gh is not usable, or "".
func (g *githubCLI) authReason(ctx context.Context) string {
	if _, err := g.run(ctx, 30*time.Second, "auth", "status"); err != nil {
		return "the GitHub CLI is not authenticated — run `gh auth login`"
	}
	return ""
}

// existingPR looks for a pull request already open for the branch.
func (g *githubCLI) existingPR(ctx context.Context, req Request) *Result {
	out, err := g.run(ctx, 60*time.Second,
		"pr", "list", "--repo", req.Remote.Slug(), "--head", req.Head,
		"--state", "open", "--json", "number,url", "--limit", "1")
	if err != nil {
		return nil
	}
	var found []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &found) != nil || len(found) == 0 {
		return nil
	}
	return &Result{Opened: true, URL: found[0].URL, Number: found[0].Number,
		Reason: "a pull request for this branch was already open"}
}

func (g *githubCLI) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, g.binary(), args...)
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = err.Error()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("gh %s: %s", strings.Join(args, " "), firstLine(message))
		}
		return "", fmt.Errorf("run gh: %w", err)
	}
	return stdout.String(), nil
}

// lastURL pulls the pull-request URL out of gh's chatty output, which prints
// progress lines before the URL it created.
func lastURL(out string) string {
	var found string
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "https://") {
			found = field
		}
	}
	return found
}

func numberFromURL(url string) int {
	index := strings.LastIndex(url, "/")
	if index < 0 {
		return 0
	}
	number := 0
	for _, r := range url[index+1:] {
		if r < '0' || r > '9' {
			return 0
		}
		number = number*10 + int(r-'0')
	}
	return number
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
