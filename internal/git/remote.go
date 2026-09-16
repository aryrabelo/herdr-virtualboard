package git

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Kind is the forge a remote points at.
type Kind string

const (
	GitHub  Kind = "github"
	Forgejo Kind = "forgejo"
	Gitea   Kind = "gitea"
	GitLab  Kind = "gitlab"
	Unknown Kind = "unknown"
)

// Remote is a parsed git remote URL.
type Remote struct {
	// Raw is the URL exactly as git reports it.
	Raw string `json:"raw"`
	// Kind is the forge this appears to be.
	Kind Kind `json:"kind"`
	// Host is the hostname, without any port.
	Host string `json:"host"`
	// Owner and Name are the repository path segments.
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// Slug is `owner/name`.
func (r Remote) Slug() string {
	if r.Owner == "" {
		return r.Name
	}
	return r.Owner + "/" + r.Name
}

// isLocalPath reports whether a remote is a filesystem path rather than a URL.
// Windows drive letters are deliberately not handled: hvb supports Linux and
// macOS, and treating `C:/repo` as scp-like there would be the right answer
// anyway for the platforms it does support.
func isLocalPath(raw string) bool {
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "./") ||
		strings.HasPrefix(raw, "../") || strings.HasPrefix(raw, "~") {
		return true
	}
	// A bare relative path such as `../sibling.git` is legal, but so is a
	// line of prose, and the two are only distinguishable by shape. An
	// absolute path above may contain spaces; a relative one may not, which
	// keeps garbage from being accepted as a remote.
	return !strings.Contains(raw, ":") && !strings.ContainsAny(raw, " \t")
}

// scpLike matches git's `user@host:path` syntax, which is not a URL and which
// url.Parse silently mangles into something that looks parsed but is not.
var scpLike = regexp.MustCompile(`^(?:([^@/]+)@)?([^:/]+):(.+)$`)

// ParseRemote decodes a remote URL into its parts and guesses the forge.
//
// The guess is a hint for choosing an API client, never a security decision.
// A self-hosted Forgejo has an arbitrary hostname — `forgejo.example.internal`
// is as likely as anything — so the host is only consulted for the forges with
// well-known domains, and everything else stays Unknown until the user names
// the kind in configuration.
func ParseRemote(raw string) (Remote, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Remote{}, fmt.Errorf("empty remote URL")
	}
	remote := Remote{Raw: trimmed, Kind: Unknown}

	var host, path string
	switch {
	case strings.Contains(trimmed, "://"):
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return Remote{}, fmt.Errorf("parse remote %q: %w", trimmed, err)
		}
		host, path = parsed.Hostname(), parsed.Path
	case isLocalPath(trimmed):
		// A filesystem path is a perfectly ordinary git remote — a bare
		// repository on disk, a mounted share — and it is what the local
		// test path uses. It has no host, so nothing web-facing can be
		// derived from it, which the caller handles.
		host, path = "", trimmed
	default:
		match := scpLike.FindStringSubmatch(trimmed)
		if match == nil {
			return Remote{}, fmt.Errorf("unrecognised remote URL %q", trimmed)
		}
		host, path = match[2], match[3]
	}

	remote.Host = host
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if index := strings.LastIndex(path, "/"); index >= 0 {
		remote.Owner, remote.Name = path[:index], path[index+1:]
	} else {
		remote.Name = path
	}
	// A self-hosted forge can live under a sub-path; only the last two segments
	// are the repository, and the rest belongs to the host's routing.
	if segments := strings.Split(remote.Owner, "/"); len(segments) > 1 {
		remote.Owner = segments[len(segments)-1]
	}

	remote.Kind = kindForHost(host)
	return remote, nil
}

func kindForHost(host string) Kind {
	lowered := strings.ToLower(host)
	switch {
	case lowered == "github.com", strings.HasSuffix(lowered, ".github.com"):
		return GitHub
	case lowered == "gitlab.com", strings.HasSuffix(lowered, ".gitlab.com"):
		return GitLab
	case strings.Contains(lowered, "forgejo"):
		return Forgejo
	case strings.Contains(lowered, "gitea"):
		return Gitea
	default:
		// A self-hosted forge on a neutral hostname is indistinguishable from
		// here. Saying so is more useful than guessing wrong and failing at
		// the API call with a confusing error.
		return Unknown
	}
}

// WebURL is the repository's https base, derived from the remote. It is what
// compare and pull-request links are built from, and it is the fallback hvb
// hands the user when it cannot open a pull request itself.
func (r Remote) WebURL() string {
	if r.Host == "" || r.Slug() == "" {
		return ""
	}
	return "https://" + r.Host + "/" + r.Slug()
}

// CompareURL is the page for opening a pull request by hand.
func (r Remote) CompareURL(base, branch string) string {
	web := r.WebURL()
	if web == "" {
		return ""
	}
	switch r.Kind {
	case GitLab:
		return fmt.Sprintf("%s/-/merge_requests/new?merge_request[source_branch]=%s&merge_request[target_branch]=%s",
			web, url.QueryEscape(branch), url.QueryEscape(base))
	case Forgejo, Gitea:
		return fmt.Sprintf("%s/compare/%s...%s", web, url.PathEscape(base), url.PathEscape(branch))
	default:
		return fmt.Sprintf("%s/compare/%s...%s?expand=1", web, url.PathEscape(base), url.PathEscape(branch))
	}
}
