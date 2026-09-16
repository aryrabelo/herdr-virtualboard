package git

import (
	"regexp"
	"strings"
	"unicode"
)

// BranchTemplate is the default branch name for a feature. It matches the
// convention VirtualBoard's own `scripts/worktree-setup.sh` uses, so a branch
// hvb creates is indistinguishable from one made by hand.
const BranchTemplate = "feature/{id}/{slug}"

// BranchName renders a template for a feature. `{id}` is the feature id as
// written in frontmatter, `{slug}` a kebab-case form of the title, and
// `{id_lower}` the lowercased id for templates that prefer it.
func BranchName(template, id, title string) string {
	if strings.TrimSpace(template) == "" {
		template = BranchTemplate
	}
	replacer := strings.NewReplacer(
		"{id}", id,
		"{id_lower}", strings.ToLower(id),
		"{slug}", Slug(title),
	)
	return sanitizeRef(replacer.Replace(template))
}

// Slug converts a feature title to a kebab-case branch segment.
func Slug(title string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	// Long enough to identify the feature, short enough that the branch and
	// the worktree directory stay readable.
	const maxSlug = 48
	if len(slug) > maxSlug {
		slug = slug[:maxSlug]
		if index := strings.LastIndexByte(slug, '-'); index > 0 {
			slug = slug[:index]
		}
	}
	if slug == "" {
		slug = "feature"
	}
	return slug
}

// refInvalid matches what git refuses in a ref name: whitespace and the
// characters git-check-ref-format rejects outright.
var refInvalid = regexp.MustCompile(`[\x00-\x20~^:?*\[\\]+`)

// sanitizeRef makes a branch name git will accept, applying the rules from
// git-check-ref-format that a generated name can plausibly violate.
func sanitizeRef(name string) string {
	name = refInvalid.ReplaceAllString(name, "-")
	name = strings.ReplaceAll(name, "..", "-")
	name = strings.ReplaceAll(name, "@{", "-")
	for strings.Contains(name, "//") {
		name = strings.ReplaceAll(name, "//", "/")
	}
	name = strings.Trim(name, "/.-")
	name = strings.TrimSuffix(name, ".lock")
	if name == "" {
		return "feature"
	}
	return name
}
