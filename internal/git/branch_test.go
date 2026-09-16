package git

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBranchNameFollowsTheVirtualBoardConvention(t *testing.T) {
	got := BranchName("", "FTR-0042", "Add retry to the uploader")
	if got != "feature/FTR-0042/add-retry-to-the-uploader" {
		t.Fatalf("BranchName = %q", got)
	}
}

func TestBranchNameTemplates(t *testing.T) {
	cases := map[string]string{
		"{id}/{slug}":      "FTR-0007/add-retry",
		"agent/{id_lower}": "agent/ftr-0007",
		"wip/{slug}":       "wip/add-retry",
	}
	for template, want := range cases {
		if got := BranchName(template, "FTR-0007", "Add retry"); got != want {
			t.Errorf("BranchName(%q) = %q, want %q", template, got, want)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Add retry to the uploader":  "add-retry-to-the-uploader",
		"  Spaces   everywhere  ":    "spaces-everywhere",
		"Punctuation! And? Symbols#": "punctuation-and-symbols",
		"CamelCase Title":            "camelcase-title",
		"":                           "feature",
		"!!!":                        "feature",
		"café münchen":               "café-münchen",
	}
	for title, want := range cases {
		if got := Slug(title); got != want {
			t.Errorf("Slug(%q) = %q, want %q", title, got, want)
		}
	}
}

// A very long title must not produce an unreadable branch, and must not be cut
// mid-word.
func TestSlugIsBounded(t *testing.T) {
	slug := Slug(strings.Repeat("verylongword ", 20))
	if len(slug) > 48 {
		t.Fatalf("slug is %d characters: %q", len(slug), slug)
	}
	if strings.HasSuffix(slug, "-") {
		t.Fatalf("slug ends mid-word: %q", slug)
	}
}

// Whatever a title contains, the branch name has to be one git will accept.
func TestBranchNamesAreValidRefs(t *testing.T) {
	titles := []string{
		"Add retry to the uploader",
		"Fix the ~tilde^caret:colon?question*star[bracket]",
		"Trailing dots...",
		"Double//slashes",
		"@{weird}",
		"ends with .lock",
		"   ",
	}
	for _, title := range titles {
		branch := BranchName("", "FTR-0001", title)
		cmd := exec.Command("git", "check-ref-format", "--branch", branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("title %q produced invalid branch %q: %v\n%s", title, branch, err, out)
		}
	}
}
