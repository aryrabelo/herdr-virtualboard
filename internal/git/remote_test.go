package git

import "testing"

func TestParseRemote(t *testing.T) {
	cases := []struct {
		raw   string
		kind  Kind
		host  string
		owner string
		name  string
	}{
		{"git@github.com:virtualboard/herdr-virtualboard.git", GitHub, "github.com", "virtualboard", "herdr-virtualboard"},
		{"https://github.com/virtualboard/herdr-virtualboard.git", GitHub, "github.com", "virtualboard", "herdr-virtualboard"},
		{"https://github.com/virtualboard/herdr-virtualboard", GitHub, "github.com", "virtualboard", "herdr-virtualboard"},
		// A self-hosted Forgejo over ssh:// with a non-default port — the
		// shape that broke naive url.Parse handling.
		{"ssh://git@forgejo.internal.example:2222/TeamOrg/service.git", Forgejo, "forgejo.internal.example", "TeamOrg", "service"},
		{"git@gitlab.com:group/project.git", GitLab, "gitlab.com", "group", "project"},
		{"https://gitea.example.com/team/thing.git", Gitea, "gitea.example.com", "team", "thing"},
		// A self-hosted forge on a neutral hostname is genuinely unknowable
		// from the URL, and saying so beats guessing wrong.
		{"git@code.internal:team/thing.git", Unknown, "code.internal", "team", "thing"},
	}
	for _, tc := range cases {
		remote, err := ParseRemote(tc.raw)
		if err != nil {
			t.Errorf("ParseRemote(%q): %v", tc.raw, err)
			continue
		}
		if remote.Kind != tc.kind || remote.Host != tc.host || remote.Owner != tc.owner || remote.Name != tc.name {
			t.Errorf("ParseRemote(%q) = %+v, want kind=%s host=%s owner=%s name=%s",
				tc.raw, remote, tc.kind, tc.host, tc.owner, tc.name)
		}
	}
}

// A port in an ssh:// URL must not end up in the host, or every derived web
// and API URL is wrong.
func TestParseRemoteDropsThePort(t *testing.T) {
	remote, err := ParseRemote("ssh://git@forgejo.internal.example:2222/TeamOrg/service.git")
	if err != nil {
		t.Fatal(err)
	}
	if remote.Host != "forgejo.internal.example" {
		t.Fatalf("Host = %q, want the port stripped", remote.Host)
	}
	if got := remote.WebURL(); got != "https://forgejo.internal.example/TeamOrg/service" {
		t.Fatalf("WebURL = %q", got)
	}
}

func TestParseRemoteRejectsNonsense(t *testing.T) {
	for _, raw := range []string{"", "   ", "not a url at all"} {
		if _, err := ParseRemote(raw); err == nil {
			t.Errorf("ParseRemote(%q) should fail", raw)
		}
	}
}

// A local bare repository as origin is what the local test path uses; it has no
// host, so there is nothing to build a web URL from and that must not panic.
func TestParseRemoteHandlesALocalPath(t *testing.T) {
	remote, err := ParseRemote("/tmp/origin.git")
	if err != nil {
		t.Fatalf("a local path remote should parse: %v", err)
	}
	if remote.Kind != Unknown {
		t.Errorf("Kind = %q, want unknown", remote.Kind)
	}
	if got := remote.CompareURL("main", "topic"); got != "" {
		t.Errorf("CompareURL for a host-less remote = %q, want empty", got)
	}
}

func TestCompareURLPerForge(t *testing.T) {
	github, _ := ParseRemote("git@github.com:o/r.git")
	if got := github.CompareURL("main", "feature/FTR-0001/x"); got != "https://github.com/o/r/compare/main...feature%2FFTR-0001%2Fx?expand=1" {
		t.Errorf("github compare = %q", got)
	}
	forgejo, _ := ParseRemote("ssh://git@forgejo.example:2222/o/r.git")
	if got := forgejo.CompareURL("main", "topic"); got != "https://forgejo.example/o/r/compare/main...topic" {
		t.Errorf("forgejo compare = %q", got)
	}
	gitlab, _ := ParseRemote("git@gitlab.com:o/r.git")
	if got := gitlab.CompareURL("main", "topic"); got == "" || got[:30] != "https://gitlab.com/o/r/-/merge" {
		t.Errorf("gitlab compare = %q", got)
	}
}
