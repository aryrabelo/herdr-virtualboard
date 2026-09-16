package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/git"
)

// giteaAPI opens pull requests on Forgejo and Gitea, which share an API.
//
// There is no official CLI for either that is reliably installed, so this talks
// to the REST endpoint directly. It needs a token, and not having one is the
// common case rather than an error — self-hosted forges are usually reached
// over SSH with no HTTP credentials configured at all.
type giteaAPI struct {
	token   string
	baseURL string
	kind    git.Kind
	client  *http.Client
}

func (g *giteaAPI) Name() string { return string(g.kind) }

func (g *giteaAPI) httpClient() *http.Client {
	if g.client != nil {
		return g.client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// base returns the API root for the instance.
func (g *giteaAPI) base(remote git.Remote) string {
	if g.baseURL != "" {
		return strings.TrimSuffix(g.baseURL, "/")
	}
	// The SSH remote's host is reused over https. A forge published on a
	// different hostname or under a path needs the explicit base URL, which
	// is what the configuration key is for.
	return "https://" + remote.Host
}

func (g *giteaAPI) Open(ctx context.Context, req Request) (*Result, error) {
	if strings.TrimSpace(g.token) == "" {
		return &Result{Reason: fmt.Sprintf(
			"no API token for %s — set forge.token in config or $HVB_FORGE_TOKEN, "+
				"or open the pull request from the compare page", req.Remote.Host)}, nil
	}

	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls",
		g.base(req.Remote), url.PathEscape(req.Remote.Owner), url.PathEscape(req.Remote.Name))

	payload := map[string]any{
		"base":  req.Base,
		"head":  req.Head,
		"title": req.Title,
		"body":  req.Body,
	}
	// Forgejo and Gitea do not accept a `draft` field on creation; a draft is
	// expressed as a title prefix, which is what their own web UI does.
	if req.Draft && !strings.HasPrefix(strings.ToUpper(req.Title), "WIP:") {
		payload["title"] = "WIP: " + req.Title
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode pull-request payload: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build pull-request request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "token "+g.token)

	response, err := g.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w", endpoint, err)
	}
	defer response.Body.Close()

	// Bounded: a misconfigured base URL can point at something that streams.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", endpoint, err)
	}

	switch {
	case response.StatusCode == http.StatusCreated, response.StatusCode == http.StatusOK:
		var created struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
		}
		if err := json.Unmarshal(raw, &created); err != nil {
			return nil, fmt.Errorf("decode pull-request response: %w", err)
		}
		return &Result{Opened: true, URL: created.HTMLURL, Number: created.Number}, nil

	case response.StatusCode == http.StatusConflict:
		// A pull request for this head already exists, which is a success.
		return &Result{Opened: true, Reason: "a pull request for this branch was already open"}, nil

	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return &Result{Reason: fmt.Sprintf("the %s token was rejected (%s) — check it has write access to %s",
			g.kind, response.Status, req.Remote.Slug())}, nil

	case response.StatusCode == http.StatusNotFound:
		return &Result{Reason: fmt.Sprintf("%s returned 404 for %s — check the repository path and the API base URL",
			g.kind, endpoint)}, nil

	default:
		return nil, fmt.Errorf("%s replied %s: %s", g.kind, response.Status, snippet(raw))
	}
}

func snippet(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return firstLine(text)
}
