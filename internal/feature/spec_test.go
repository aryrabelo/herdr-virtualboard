package feature

import (
	"strings"
	"testing"
)

const sampleSpec = `---
id: FTR-0007
title: Add retry to the uploader
status: in-progress
owner: alice
priority: P1
complexity: L
created: 2026-01-02
updated: 2026-03-04
labels:
  - backend
  - reliability
dependencies:
  - FTR-0003
epic: EP-0001
risk_notes: touches the upload hot path
---

# Feature Spec: Add retry to the uploader

<untrusted-content>

## Summary
Retry failed PUTs with backoff.

## Acceptance Criteria (Testable)
- [x] Failed PUTs retry three times
- [ ] Backoff is exponential
- [ ] A unit test covers the retry path

## Implementation Notes
Reuse the helper in internal/http.

</untrusted-content>

## Links
- FTR-0003
`

func TestParseFrontmatter(t *testing.T) {
	spec, err := Parse([]byte(sampleSpec))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if spec.ID != "FTR-0007" {
		t.Errorf("ID = %q", spec.ID)
	}
	if spec.Status != InProgress {
		t.Errorf("Status = %q", spec.Status)
	}
	if spec.Priority != "P1" || spec.Complexity != "L" {
		t.Errorf("Priority/Complexity = %q/%q", spec.Priority, spec.Complexity)
	}
	if len(spec.Labels) != 2 || spec.Labels[0] != "backend" {
		t.Errorf("Labels = %v", spec.Labels)
	}
	if len(spec.Dependencies) != 1 || spec.Dependencies[0] != "FTR-0003" {
		t.Errorf("Dependencies = %v", spec.Dependencies)
	}
	if spec.Epic != "EP-0001" {
		t.Errorf("Epic = %q", spec.Epic)
	}
	if !strings.Contains(spec.Body, "## Summary") {
		t.Errorf("Body lost its headings")
	}
	if strings.Contains(spec.Body, "id: FTR-0007") {
		t.Errorf("Body still contains the frontmatter")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"no frontmatter":         "# Just a document\n",
		"unterminated":           "---\nid: FTR-0001\n",
		"frontmatter without id": "---\ntitle: nameless\n---\nbody\n",
	}
	for name, input := range cases {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("%s: Parse succeeded, want an error", name)
		}
	}
}

func TestParseHandlesCRLF(t *testing.T) {
	spec, err := Parse([]byte(strings.ReplaceAll(sampleSpec, "\n", "\r\n")))
	if err != nil {
		t.Fatalf("Parse CRLF: %v", err)
	}
	if spec.ID != "FTR-0007" {
		t.Errorf("ID = %q", spec.ID)
	}
}

func TestSection(t *testing.T) {
	spec, err := Parse([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := spec.Section("Summary")
	if !ok || summary != "Retry failed PUTs with backoff." {
		t.Errorf("Section(Summary) = %q, %v", summary, ok)
	}
	// Case-insensitive and tolerant of the leading hashes.
	if _, ok := spec.Section("## implementation notes"); !ok {
		t.Error("Section should match case-insensitively and ignore hashes")
	}
	if body, ok := spec.Section("Nonexistent"); ok {
		t.Errorf("Section(Nonexistent) = %q, true; want not ok", body)
	}
}

// A section must stop at the next heading of the same or higher level, not run
// on into the rest of the document.
func TestSectionStopsAtNextHeading(t *testing.T) {
	spec, err := Parse([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	notes, _ := spec.Section("Implementation Notes")
	if strings.Contains(notes, "Links") {
		t.Errorf("Implementation Notes leaked into the next section: %q", notes)
	}
}

func TestAcceptanceCriteria(t *testing.T) {
	spec, err := Parse([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	criteria := spec.AcceptanceCriteria()
	if len(criteria) != 3 {
		t.Fatalf("got %d criteria, want 3: %+v", len(criteria), criteria)
	}
	if !criteria[0].Done || criteria[0].Text != "Failed PUTs retry three times" {
		t.Errorf("criteria[0] = %+v", criteria[0])
	}
	if criteria[1].Done {
		t.Errorf("criteria[1] should be unticked: %+v", criteria[1])
	}
}

func TestClaimed(t *testing.T) {
	spec := &Spec{Frontmatter: Frontmatter{Owner: "alice"}}
	if !spec.Claimed() {
		t.Error("alice should count as claimed")
	}
	for _, owner := range []string{"", "  ", Unassigned} {
		spec.Owner = owner
		if spec.Claimed() {
			t.Errorf("owner %q should not count as claimed", owner)
		}
	}
}

func TestSortSpecsByPriorityThenID(t *testing.T) {
	specs := []*Spec{
		{Frontmatter: Frontmatter{ID: "FTR-0003", Priority: "P2"}},
		{Frontmatter: Frontmatter{ID: "FTR-0001", Priority: ""}},
		{Frontmatter: Frontmatter{ID: "FTR-0002", Priority: "P0"}},
		{Frontmatter: Frontmatter{ID: "FTR-0004", Priority: "P2"}},
	}
	SortSpecs(specs)
	want := []string{"FTR-0002", "FTR-0003", "FTR-0004", "FTR-0001"}
	for index, id := range want {
		if specs[index].ID != id {
			t.Fatalf("position %d = %s, want %s (order: %v)", index, specs[index].ID, id, ids(specs))
		}
	}
}

func ids(specs []*Spec) []string {
	out := make([]string, len(specs))
	for index, spec := range specs {
		out[index] = spec.ID
	}
	return out
}
