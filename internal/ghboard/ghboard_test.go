package ghboard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// The fixture in testdata/pr_list.json is real `gh pr list --json …` output,
// captured from three repositories and read as one list, so a single test
// covers every state a card can be in:
//
//   - aryrabelo/cnb_landing_page: #179 merged by a GitHub App and linked to
//     issue 168 (this is the card in the approved print), #176 merged by the
//     repo owner, #161 and #59 closed without merging.
//   - cli/cli: #14355 an open draft by a maintainer pushing an in-repo
//     branch, #13899 and #14448 opened from forks — and note that this
//     repository has a label of its own literally named "external".
//   - spf13/cobra: #2500 opened from a fork by a human, carrying no
//     repository labels at all. That absence is the point: on #13899 a
//     HasLabel("external") check cannot tell the derived badge from the
//     repository's own label, so the External rule is proved on #2500
//     instead, where only the badge can produce that label.
const fixtureRepo = "aryrabelo/cnb_landing_page"

// fixtureNow is just after the newest event in the fixture (#179 merged at
// 2026-09-17T14:59:39Z), so windows measured from it behave as they did on the
// day the fixture was captured.
var fixtureNow = time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// stubSource returns a Source whose gh seam serves canned bytes. It also
// asserts the contract the production runner depends on: a deadline must reach
// gh, or a wedged network call would hang the board's refresh forever.
func stubSource(t *testing.T, repo string, window time.Duration, out []byte, runErr error) (*Source, *[]string) {
	t.Helper()
	var gotArgs []string
	s := New(repo, window)
	s.now = func() time.Time { return fixtureNow }
	s.run = func(ctx context.Context, args ...string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("gh was invoked without a deadline")
		}
		gotArgs = args
		return out, runErr
	}
	return s, &gotArgs
}

func specByID(t *testing.T, specs []*feature.Spec, id string) *feature.Spec {
	t.Helper()
	for _, s := range specs {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no card %s among %v", id, ids(specs))
	return nil
}

func ids(specs []*feature.Spec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}

func TestLoadAsksGhForEveryStateAndTheMeasuredFieldSet(t *testing.T) {
	s, args := stubSource(t, fixtureRepo, 0, loadFixture(t, "pr_list.json"), nil)
	if _, errs := s.Load(context.Background()); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	joined := strings.Join(*args, " ")
	// --state all is what makes cancelled work visible: gh defaults to open
	// only, which would hide every closed and merged pull request.
	for _, want := range []string{"pr list", "--repo " + fixtureRepo, "--state all", "--json " + ghFields} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh args %q missing %q", joined, want)
		}
	}
	if !strings.Contains(ghFields, "closingIssuesReferences") {
		t.Error("the linked issue must be requested in the same call, not a second graphql round trip")
	}
}

func TestLoadClassifiesEveryPullRequestState(t *testing.T) {
	s, _ := stubSource(t, fixtureRepo, 0, loadFixture(t, "pr_list.json"), nil)
	specs, errs := s.Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Newest first, so the terminal columns read as "what just happened".
	wantOrder := []string{"PR-14448", "PR-14355", "PR-13899", "PR-2500", "PR-179", "PR-176", "PR-161", "PR-59"}
	if got := ids(specs); !equalStrings(got, wantOrder) {
		t.Fatalf("card order = %v, want %v", got, wantOrder)
	}

	cases := []struct {
		id     string
		status feature.Status
		owner  string
		labels []string
		deps   []string
	}{
		{
			// Merged by a GitHub App, linked to issue 168: this is the
			// card in the approved design.
			id: "PR-179", status: feature.Done, owner: "factory-bora[bot]",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:179", LabelPrefix + "author:factory-bora[bot]", LabelMerged, LabelExternal, LabelPrefix + "issue:168", "status:auto-approved"},
			deps:   []string{"168"},
		},
		{
			// Merged by the repository owner: not External, no issue.
			id: "PR-176", status: feature.Done, owner: "aryrabelo",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:176", LabelPrefix + "author:aryrabelo", LabelMerged},
		},
		{
			// Closed without merging: terminal, but cancelled rather
			// than shipped.
			id: "PR-161", status: feature.Done, owner: "factory-bora[bot]",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:161", LabelPrefix + "author:factory-bora[bot]", LabelCanceled, LabelExternal},
		},
		{
			// Cancelled and still carrying its linked issue.
			id: "PR-59", status: feature.Done, owner: "aryrabelo",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:59", LabelPrefix + "author:aryrabelo", LabelCanceled, LabelPrefix + "issue:58"},
			deps:   []string{"58"},
		},
		{
			// Open from a fork, and the repository has its own label
			// literally named "external". Now that the minted badge is
			// prefixed the two no longer collide: both appear, and
			// they are distinguishable.
			id: "PR-13899", status: feature.Review, owner: "imkp1",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:13899", LabelPrefix + "author:imkp1", LabelOpen, LabelExternal, LabelPrefix + "issue:cli/cli#13804", "external", "gh-issue", "ready-for-review"},
			deps:   []string{"cli/cli#13804"},
		},
		{
			// Open draft: still Review, flagged so the card can say so.
			// Not External: a human pushing a branch into the
			// repository itself necessarily has push access, so this
			// is a maintainer even though the repository belongs to an
			// organisation nobody's login matches.
			id: "PR-14355", status: feature.Review, owner: "williammartin",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:14355", LabelPrefix + "author:williammartin", LabelOpen, LabelDraft},
		},
		{
			id: "PR-14448", status: feature.Done, owner: "vimalyad",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:14448", LabelPrefix + "author:vimalyad", LabelCanceled, LabelExternal, LabelPrefix + "issue:cli/cli#14386", "external", "unmet-requirements"},
			deps:   []string{"cli/cli#14386"},
		},
		{
			// Open from a fork by a human, and the repository has no
			// labels at all, so the only way "external" can appear
			// here is the derived badge.
			id: "PR-2500", status: feature.Review, owner: "zzz-yu",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:2500", LabelPrefix + "author:zzz-yu", LabelOpen, LabelExternal, LabelPrefix + "issue:spf13/cobra#706"},
			deps:   []string{"spf13/cobra#706"},
		},
	}

	for _, tc := range cases {
		spec := specByID(t, specs, tc.id)
		if spec.Status != tc.status {
			t.Errorf("%s status = %q, want %q", tc.id, spec.Status, tc.status)
		}
		if spec.Owner != tc.owner {
			t.Errorf("%s owner = %q, want %q", tc.id, spec.Owner, tc.owner)
		}
		if !equalStrings(spec.Labels, tc.labels) {
			t.Errorf("%s labels = %v, want %v", tc.id, spec.Labels, tc.labels)
		}
		if !equalStrings(spec.Dependencies, tc.deps) {
			t.Errorf("%s dependencies = %v, want %v", tc.id, spec.Dependencies, tc.deps)
		}
		if spec.Title == "" {
			t.Errorf("%s has no title", tc.id)
		}
		if spec.Path != "" {
			t.Errorf("%s path = %q, want empty: a pull request has no spec file", tc.id, spec.Path)
		}
		// The card renders an age, so the timestamps must be parseable
		// without a second convention.
		if _, err := time.Parse(time.RFC3339, spec.Created); err != nil {
			t.Errorf("%s created %q is not RFC3339: %v", tc.id, spec.Created, err)
		}
		if _, err := time.Parse(time.RFC3339, spec.Updated); err != nil {
			t.Errorf("%s updated %q is not RFC3339: %v", tc.id, spec.Updated, err)
		}
		if spec.Status != feature.Done && spec.Status != feature.Review {
			t.Errorf("%s status %q is outside the two states a pull request can occupy", tc.id, spec.Status)
		}
	}
}

// External is decided by fork-ness or bot-ness, and by nothing else. Two
// plausible rival rules exist and both are wrong; this table kills each with a
// real pull request.
func TestExternalIsForkOrBotAndNotOwnerMismatch(t *testing.T) {
	s, _ := stubSource(t, fixtureRepo, 0, loadFixture(t, "pr_list.json"), nil)
	specs, _ := s.Load(context.Background())

	cases := []struct {
		id       string
		external bool
		why      string
	}{
		{
			id: "PR-179", external: true,
			// isCrossRepository is false here: factory-bora pushes its
			// branch into the repository itself. A rule reading that
			// field would drop the badge the approved print shows.
			why: "a GitHub App pushing in-repo is still not a teammate",
		},
		{
			id: "PR-14355", external: false,
			// author williammartin never equals repository owner
			// "cli", so an owner-mismatch rule would badge this
			// maintainer External — and every other card in an
			// organisation repository with it.
			why: "a human pushing an in-repo branch must have push access",
		},
		{
			id: "PR-2500", external: true,
			// spf13/cobra carries no labels of its own on this pull
			// request, so HasLabel here can only be seeing the badge.
			// PR-13899 and PR-14448 are forks too, but cli/cli has a
			// label literally named "external", which would make this
			// assertion pass even with the badge gone — measured: a
			// mutant that dropped the fork rule slipped past an
			// earlier version of this test that used them.
			why: "opened from a fork, on a repository with no labels of its own",
		},
		{id: "PR-176", external: false, why: "the repository owner"},
	}

	for _, tc := range cases {
		if got := specByID(t, specs, tc.id).HasLabel(LabelExternal); got != tc.external {
			t.Errorf("%s external = %v, want %v: %s", tc.id, got, tc.external, tc.why)
		}
	}
}

// A repository label is content anybody can type in the GitHub UI. If one
// could claim the reserved prefix it would forge a verdict the board reads as
// the harness's own — putting a merged pull request in the cancelled column,
// or badging a teammate's work External. The fixture is a real merged pull
// request (aryrabelo/cnb_landing_page#176, renumbered) wearing five repository
// labels: four that claim the prefix — including `hvb:inventado-amanha`, a
// name this package does not mint at all — and one honest one.
func TestReservedPrefixCannotArriveFromTheRepository(t *testing.T) {
	s, _ := stubSource(t, fixtureRepo, 0, loadFixture(t, "pr_list_forged_labels.json"), nil)
	specs, errs := s.Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d cards, want 1", len(specs))
	}
	card := specs[0]

	// The pull request is merged and authored by a human pushing an in-repo
	// branch, so the honest verdict is Done, not cancelled, and not External.
	if card.Status != feature.Done {
		t.Errorf("status = %q, want %q", card.Status, feature.Done)
	}
	if card.HasLabel(LabelCanceled) {
		t.Error("a repository label forged the cancelled verdict: this card would sit in the terminal column")
	}
	if card.HasLabel(LabelExternal) {
		t.Error("a repository label forged the External badge")
	}

	// Discarding the forgeries must not cost the honest label.
	if !card.HasLabel("needs-review") {
		t.Errorf("labels = %v, want the repository's own \"needs-review\" kept", card.Labels)
	}
	want := []string{LabelSourcePR, LabelPrefix + "pr:9176", LabelPrefix + "author:aryrabelo", LabelMerged, "needs-review"}
	if !equalStrings(card.Labels, want) {
		t.Errorf("labels = %v, want %v", card.Labels, want)
	}
}

// claimsPrefix is the tests' own reading of "this label claims the reserved
// namespace": trimmed and case-folded, spelled here rather than borrowed from
// claimsReservedPrefix. A test that classified labels with the function under
// test would be disarmed by the very mutation it exists to catch — reverting
// the discard to a byte-exact prefix would also make these sweeps file
// `HVB:STATE:MERGED` under "came from the repository" and pass.
func claimsPrefix(label string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(label)), LabelPrefix)
}

// The namespace rule stated once, over every fixture, without naming a single
// label — so it still holds for whatever label this package mints in six
// months. Each label on a card is classified by where it can have come from:
//
//	minted here  → must wear the reserved prefix, and must NOT be a label the
//	               repository also sent, or the repository forged it
//	from the repo → must appear verbatim in that pull request's own labels
//
// Half (a) is caught by an unprefixed label that the repository never sent.
// Half (b) is caught by a prefixed label that the repository did send. Neither
// half consults a list of names, and a claim written in another case or padded
// with spaces counts as a claim.
func TestEveryMintedLabelIsInTheHvbNamespace(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures to sweep: %v", err)
	}

	mintedSeen, repoSeen, discarded := 0, 0, 0
	for _, path := range fixtures {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var prs []*pullRequest
		if err := json.Unmarshal(raw, &prs); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		sent := make(map[string]map[string]bool, len(prs))
		for _, pr := range prs {
			names := make(map[string]bool, len(pr.Labels))
			for _, l := range pr.Labels {
				names[l.Name] = true
				if claimsPrefix(l.Name) {
					discarded++
				}
			}
			sent["PR-"+strconv.Itoa(pr.Number)] = names
		}

		s, _ := stubSource(t, fixtureRepo, 0, raw, nil)
		specs, errs := s.Load(context.Background())
		if len(errs) != 0 {
			t.Fatalf("%s: unexpected errors: %v", path, errs)
		}
		for _, spec := range specs {
			fromRepo := sent[spec.ID]
			for _, label := range spec.Labels {
				if claimsPrefix(label) {
					mintedSeen++
					if fromRepo[label] {
						t.Errorf("%s: %s: label %q wears the reserved prefix and the repository sent it too — the discard let a forgery through", path, spec.ID, label)
					}
					continue
				}
				repoSeen++
				if !fromRepo[label] {
					t.Errorf("%s: %s: label %q was minted by this package but is outside the %q namespace", path, spec.ID, label, LabelPrefix)
				}
			}
		}
	}

	// A sweep that saw only one kind of label would pass while proving
	// nothing about the other half.
	if mintedSeen == 0 || repoSeen == 0 || discarded == 0 {
		t.Fatalf("sweep is vacuous: %d minted, %d from repositories, %d reserved-prefix labels offered by a repository", mintedSeen, repoSeen, discarded)
	}
}

// The repository owner must not participate in the External decision at all,
// so reading the same pull requests as a different repository cannot move a
// single badge. This fails the moment owner-mismatch creeps back in.
func TestExternalIgnoresWhichRepositoryIsBeingRead(t *testing.T) {
	fixture := loadFixture(t, "pr_list.json")

	var first []string
	for _, repo := range []string{"aryrabelo/cnb_landing_page", "cli/cli", "someone-else/whatever"} {
		s, _ := stubSource(t, repo, 0, fixture, nil)
		specs, errs := s.Load(context.Background())
		if len(errs) != 0 {
			t.Fatalf("%s: unexpected errors: %v", repo, errs)
		}
		badged := make([]string, 0, len(specs))
		for _, spec := range specs {
			if spec.HasLabel(LabelExternal) {
				badged = append(badged, spec.ID)
			}
		}
		if first == nil {
			first = badged
			if len(first) == 0 {
				t.Fatal("no card is External, so this test proves nothing")
			}
			continue
		}
		if !equalStrings(badged, first) {
			t.Errorf("read as %s the External cards are %v, want %v", repo, badged, first)
		}
	}
}

// A pull request opened before the window but merged inside it is this week's
// work; one merely opened long ago and never touched is not.
func TestLoadWindowCountsOpenedMergedOrClosed(t *testing.T) {
	cases := []struct {
		name   string
		window time.Duration
		want   []string
	}{
		{
			// PR-13899 was opened 2026-07-16 and is still open;
			// PR-14355 was opened 2026-09-04. Neither was merged or
			// closed inside the week, so neither is this week's work.
			name:   "one week",
			window: 7 * 24 * time.Hour,
			want:   []string{"PR-14448", "PR-2500", "PR-179", "PR-176", "PR-161", "PR-59"},
		},
		{
			// Cutoff 2026-09-14T15:00Z. PR-14448 was opened and closed
			// twenty minutes before it, PR-59 three days before.
			name:   "three days",
			window: 3 * 24 * time.Hour,
			want:   []string{"PR-2500", "PR-179", "PR-176", "PR-161"},
		},
		{
			// Cutoff 2026-09-17T03:00Z. PR-161 was opened
			// 2026-09-16T17:30Z, before the cutoff, and closed
			// 2026-09-17T08:11Z, after it. Closing is the only thing
			// that puts it on the board in this window.
			name:   "closing inside the window admits a card opened before it",
			window: 12 * time.Hour,
			want:   []string{"PR-179", "PR-176", "PR-161"},
		},
		{
			// Cutoff 2026-09-17T14:20Z. PR-179 was opened
			// 2026-09-17T14:16:17Z, before the cutoff, and merged
			// 2026-09-17T14:59:39Z, after it. Merging is the only
			// thing that admits it; PR-176 merged hours earlier and
			// drops out.
			name:   "merging inside the window admits a card opened before it",
			window: 40 * time.Minute,
			want:   []string{"PR-179"},
		},
		{
			name:   "no window means no age filter",
			window: 0,
			want:   []string{"PR-14448", "PR-14355", "PR-13899", "PR-2500", "PR-179", "PR-176", "PR-161", "PR-59"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := stubSource(t, fixtureRepo, tc.window, loadFixture(t, "pr_list.json"), nil)
			specs, errs := s.Load(context.Background())
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if got := ids(specs); !equalStrings(got, tc.want) {
				t.Errorf("cards = %v, want %v", got, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoadSurfacesGhFailureWithoutCards(t *testing.T) {
	s, _ := stubSource(t, fixtureRepo, 0, nil, errors.New("gh exited 4: gh: Not logged in to any GitHub hosts"))
	specs, errs := s.Load(context.Background())
	if len(specs) != 0 {
		t.Fatalf("got %d cards from a failed gh, want 0", len(specs))
	}
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", errs)
	}
	if !strings.Contains(errs[0].Error(), "Not logged in") {
		t.Errorf("error %q hides gh's own diagnosis", errs[0])
	}
}

func TestLoadRejectsMalformedRepoWithoutRunningGh(t *testing.T) {
	for _, repo := range []string{"", "cnb_landing_page", "/name", "owner/", "a/b/c"} {
		s := New(repo, 0)
		s.run = func(context.Context, ...string) ([]byte, error) {
			t.Fatalf("gh was invoked for repo %q", repo)
			return nil, nil
		}
		specs, errs := s.Load(context.Background())
		if len(specs) != 0 || len(errs) != 1 {
			t.Errorf("repo %q: got %d cards and %d errors, want 0 and 1", repo, len(specs), len(errs))
		}
	}
}

func TestLoadReportsUnparseableJSON(t *testing.T) {
	s, _ := stubSource(t, fixtureRepo, 0, []byte("not json"), nil)
	specs, errs := s.Load(context.Background())
	if len(specs) != 0 || len(errs) != 1 {
		t.Fatalf("got %d cards and %d errors, want 0 and 1", len(specs), len(errs))
	}
}

// A single unusable entry must cost its own card and nothing more.
func TestLoadKeepsGoingPastABrokenEntry(t *testing.T) {
	payload := []byte(`[
	  {"number":0,"title":"no number","createdAt":"2026-09-17T10:00:00Z"},
	  {"number":7,"title":"bad clock","createdAt":"yesterday"},
	  {"number":8,"title":"no clock at all"},
	  {"number":9,"title":"fine","state":"OPEN","createdAt":"2026-09-17T10:00:00Z","updatedAt":"2026-09-17T10:00:00Z","author":{"login":"aryrabelo","is_bot":false}}
	]`)
	s, _ := stubSource(t, fixtureRepo, 0, payload, nil)
	specs, errs := s.Load(context.Background())

	if got := ids(specs); !equalStrings(got, []string{"PR-9"}) {
		t.Fatalf("cards = %v, want [PR-9]", got)
	}
	if len(errs) != 3 {
		t.Fatalf("errors = %v, want three (no number, bad clock, no clock)", errs)
	}
	joined := errs[0].Error() + errs[1].Error() + errs[2].Error()
	for _, want := range []string{"no number", "#7", "#8"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors %q do not name %q", joined, want)
		}
	}
}

func TestBodyLeadsWithURLAndBranchAndTruncates(t *testing.T) {
	s, _ := stubSource(t, fixtureRepo, 0, loadFixture(t, "pr_list.json"), nil)
	specs, _ := s.Load(context.Background())

	card := specByID(t, specs, "PR-179")
	if !strings.HasPrefix(card.Body, "URL: https://github.com/aryrabelo/cnb_landing_page/pull/179\nBranch: factory/issue-168\n") {
		t.Errorf("PR-179 body does not lead with URL and branch:\n%q", firstLines(card.Body, 3))
	}
	if !strings.Contains(card.Body, "Closes #168") {
		t.Error("PR-179 body lost the description")
	}

	// PR-14355's real description is 5860 bytes; the detail pane gets an
	// opening, not a design document.
	long := specByID(t, specs, "PR-14355")
	if !strings.HasSuffix(long.Body, truncationMarker) {
		t.Errorf("PR-14355 body was not truncated (len %d)", len(long.Body))
	}
	if len(long.Body) > maxBodyBytes+256 {
		t.Errorf("PR-14355 body is %d bytes, want at most %d plus the header", len(long.Body), maxBodyBytes)
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}

func TestTruncateNeverSplitsARune(t *testing.T) {
	// "é" is two bytes, so a byte-exact cut at an odd limit lands mid-rune.
	s := strings.Repeat("é", 10)
	for limit := 1; limit <= len(s); limit++ {
		got := truncate(s, limit)
		if !utf8.ValidString(got) {
			t.Fatalf("limit %d produced invalid UTF-8: %q", limit, got)
		}
	}
}

// A card's label list is a set: a consumer iterating it must never render the
// same badge twice, whatever order the callers applied. This lives on the type
// because no pull request gh can return exhibits a duplicate any more — the
// reserved prefix removed the collision that used to prove it — and a fixture
// would have to be falsified to bring one back.
func TestLabelSetKeepsOneOfEachInFirstInsertionOrder(t *testing.T) {
	set := newLabelSet(4)
	// LabelExternal twice is the real shape of the risk: two branches that
	// both decide a pull request came from outside.
	for _, label := range []string{LabelSourcePR, LabelExternal, "gh-issue", LabelExternal, LabelSourcePR, "gh-issue"} {
		set.add(label)
	}

	want := []string{LabelSourcePR, LabelExternal, "gh-issue"}
	if got := set.slice(); !equalStrings(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}

	// Blank and whitespace-only labels are not labels; an empty entry would
	// render as an empty badge.
	blank := newLabelSet(2)
	blank.add("")
	blank.add("   ")
	if got := blank.slice(); got != nil {
		t.Errorf("labels = %v, want nil for blank input", got)
	}
}

// The fixture in testdata/pr_list_activity.json is the widened read — the same
// `gh pr list --json` shape plus statusCheckRollup and comments — over the
// ten cases the production line has to tell apart:
//
//	#301 merged, one green check, one comment newer than the check
//	#300 closed without merging, no checks, one comment
//	#299 open, one green check and one FAILURE, comment older than both
//	#298 open, one green check, comment NEWER than the check
//	#297 open, no checks and no comments, updatedAt bumped to 14:55
//	#296 open, one legacy StatusContext in FAILURE, no CheckRun at all
//	#295 open, one green check plus one still IN_PROGRESS
//	#294 open, one legacy StatusContext in EXPECTED and nothing else
//	#293 open, one green check plus one marked STALE
//	#292 open, one check that ended in STARTUP_FAILURE
const activityRepo = "aryrabelo/bugtoprompt"

// stateLabels are the `hvb:state:` labels on a card. Collecting them by
// prefix rather than by name is the point: a fourth state label invented later
// still has to be alone.
func stateLabels(spec *feature.Spec) []string {
	var got []string
	for _, label := range spec.Labels {
		if strings.HasPrefix(label, LabelPrefix+"state:") {
			got = append(got, label)
		}
	}
	return got
}

func labelWithPrefix(spec *feature.Spec, prefix string) (string, bool) {
	for _, label := range spec.Labels {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimPrefix(label, prefix), true
		}
	}
	return "", false
}

// The line reads the column off the state label, so a card wearing two of them
// would be in two columns at once and the declaration order — not the fact —
// would break the tie. Merged, closed-unmerged and open each produce exactly
// one, and the sweep over every card is what makes "never two" measured rather
// than assumed.
func TestEveryCardCarriesExactlyOneStateLabel(t *testing.T) {
	want := map[string]string{
		// Merged: mergedAt decides it, even though gh reports closedAt
		// on the same pull request.
		"PR-179": LabelMerged, "PR-176": LabelMerged, "PR-301": LabelMerged,
		// Closed without merging.
		"PR-161": LabelCanceled, "PR-59": LabelCanceled, "PR-14448": LabelCanceled, "PR-300": LabelCanceled,
		// Open, draft included: a draft is still open.
		"PR-13899": LabelOpen, "PR-14355": LabelOpen, "PR-2500": LabelOpen,
		"PR-299": LabelOpen, "PR-298": LabelOpen, "PR-297": LabelOpen,
		"PR-296": LabelOpen, "PR-295": LabelOpen, "PR-294": LabelOpen,
		"PR-293": LabelOpen, "PR-292": LabelOpen,
	}

	seen := map[string]int{}
	for _, fixture := range []string{"pr_list.json", "pr_list_activity.json"} {
		s, _ := stubSource(t, activityRepo, 0, loadFixture(t, fixture), nil)
		specs, errs := s.Load(context.Background())
		if len(errs) != 0 {
			t.Fatalf("%s: unexpected errors: %v", fixture, errs)
		}
		for _, spec := range specs {
			got := stateLabels(spec)
			if len(got) != 1 {
				t.Errorf("%s: %s carries %v, want exactly one state label", fixture, spec.ID, got)
				continue
			}
			if expected, known := want[spec.ID]; !known {
				t.Errorf("%s: %s is not in this test's table, so its state is unproved", fixture, spec.ID)
			} else if got[0] != expected {
				t.Errorf("%s: %s state = %q, want %q", fixture, spec.ID, got[0], expected)
			}
			seen[got[0]]++
		}
	}

	// All three have to have been exercised, or the test would pass on a
	// mutant that minted the same label for two of the states.
	for _, label := range []string{LabelMerged, LabelCanceled, LabelOpen} {
		if seen[label] == 0 {
			t.Errorf("no card produced %q, so this test proves nothing about it", label)
		}
	}
}

// The rollup is seconds-expensive, so the plain board must not pay for it: the
// assertion is on the argv gh was handed, because a parsed result cannot tell
// "the field was never requested" from "the field came back empty".
func TestWithActivityIsTheOnlyWayChecksAndCommentsAreRequested(t *testing.T) {
	plain, plainArgs := stubSource(t, activityRepo, 0, loadFixture(t, "pr_list_activity.json"), nil)
	if _, errs := plain.Load(context.Background()); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	joined := strings.Join(*plainArgs, " ")
	for _, unwanted := range []string{"statusCheckRollup", "comments"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("the plain board asked gh for %q: %s", unwanted, joined)
		}
	}

	widened, widenedArgs := stubSource(t, activityRepo, 0, loadFixture(t, "pr_list_activity.json"), nil)
	if got := widened.WithActivity(); got != widened {
		t.Error("WithActivity returned a different Source, so a caller that does not reassign gets the narrow read")
	}
	if _, errs := widened.Load(context.Background()); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	joined = strings.Join(*widenedArgs, " ")
	for _, want := range []string{"statusCheckRollup", "comments"} {
		if !strings.Contains(joined, want) {
			t.Errorf("WithActivity did not ask gh for %q: %s", want, joined)
		}
	}
	// The widened fields are an addition, not a replacement: dropping the
	// base set would cost every card its author, labels and linked issue.
	if !strings.Contains(joined, ghFields) {
		t.Errorf("WithActivity replaced the measured field set instead of widening it: %s", joined)
	}
	// A read that takes 7.9s for 20 pull requests cannot run under the
	// plain 30s budget for 200 of them.
	if widened.timeout <= defaultTimeout {
		t.Errorf("WithActivity timeout = %s, want more than the plain %s", widened.timeout, defaultTimeout)
	}
}

// Activity is what the quiet timer measures, so it has to be the newest thing
// that actually happened — and nothing else. Each row below is a different way
// the maximum can land, including the two that must mint NO label at all.
func TestActivityIsTheMaxOfTheLastCheckAndTheLastComment(t *testing.T) {
	s, _ := stubSource(t, activityRepo, 0, loadFixture(t, "pr_list_activity.json"), nil)
	s.WithActivity()
	specs, errs := s.Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	cases := []struct {
		id   string
		want string // empty means no label at all
		why  string
	}{
		{id: "PR-299", want: "2026-09-17T13:05:00Z", why: "the last check is newer than the last comment"},
		{id: "PR-298", want: "2026-09-17T14:30:00Z", why: "the last comment is newer than the last check"},
		{id: "PR-301", want: "2026-09-17T11:50:00Z", why: "merged, and the comment landed after the check"},
		{id: "PR-300", want: "2026-09-17T08:30:00Z", why: "no checks at all, so the comment is the whole measurement"},
		{id: "PR-296", want: "2026-09-17T07:00:00Z", why: "a legacy StatusContext reports in createdAt"},
		{id: "PR-295", want: "2026-09-17T06:30:00Z", why: "a running check reports in startedAt, which is newer than the finished one"},
		{id: "PR-294", want: "2026-09-17T04:00:00Z", why: "an EXPECTED StatusContext still reports a createdAt"},
		{id: "PR-293", want: "2026-09-17T03:10:00Z", why: "the STALE check completed after the green one"},
		{id: "PR-292", want: "2026-09-17T02:00:00Z", why: "a STARTUP_FAILURE still completed, and that is when it spoke"},
		{
			id: "PR-297", want: "",
			// updatedAt is 2026-09-17T14:55:00Z here, the newest
			// timestamp on the card. If it counted, this pull request
			// would look like the most active one in the fixture.
			why: "no checks and no comments: only updatedAt moved, and updatedAt is not activity",
		},
	}

	for _, tc := range cases {
		spec := specByID(t, specs, tc.id)
		got, ok := labelWithPrefix(spec, LabelActivityPrefix)
		switch {
		case tc.want == "" && ok:
			t.Errorf("%s carries %s%s but nothing was measured: %s", tc.id, LabelActivityPrefix, got, tc.why)
		case tc.want != "" && !ok:
			t.Errorf("%s carries no activity label, want %q: %s", tc.id, tc.want, tc.why)
		case tc.want != "" && got != tc.want:
			t.Errorf("%s activity = %q, want %q: %s", tc.id, got, tc.want, tc.why)
		}
	}

	// The plain field set carries neither key, and every one of those cards
	// has an updatedAt. None may claim activity: a mutant that fell back on
	// updatedAt would light up all eight of them here.
	plain, _ := stubSource(t, fixtureRepo, 0, loadFixture(t, "pr_list.json"), nil)
	narrow, errs := plain.Load(context.Background())
	if len(errs) != 0 || len(narrow) == 0 {
		t.Fatalf("plain read returned %d cards and %v", len(narrow), errs)
	}
	for _, spec := range narrow {
		if got, ok := labelWithPrefix(spec, LabelActivityPrefix); ok {
			t.Errorf("%s claims activity %q from bytes that carry no check and no comment", spec.ID, got)
		}
	}
}

// Green is a measurement, never a default. A pull request with no checks, one
// whose suite is still running, and one whose rollup holds only values that
// are not a pass — EXPECTED, STALE, STARTUP_FAILURE — all have to come back
// with neither label, so the line refuses to advance them.
func TestCheckVerdictIsMintedOnlyFromWhatWasMeasured(t *testing.T) {
	s, _ := stubSource(t, activityRepo, 0, loadFixture(t, "pr_list_activity.json"), nil)
	s.WithActivity()
	specs, errs := s.Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	cases := []struct {
		id   string
		want string // empty means neither label
		why  string
	}{
		{id: "PR-301", want: LabelCheckGreen, why: "one check, completed, SUCCESS"},
		{id: "PR-298", want: LabelCheckGreen, why: "one check, completed, SUCCESS"},
		{id: "PR-299", want: LabelCheckRed, why: "one FAILURE beside a SUCCESS: red wins"},
		{id: "PR-296", want: LabelCheckRed, why: "a legacy StatusContext in FAILURE is still a red check"},
		{id: "PR-297", want: "", why: "no checks at all is not green: nobody ran anything"},
		{id: "PR-300", want: "", why: "no checks at all"},
		{id: "PR-295", want: "", why: "a check still IN_PROGRESS has not said anything, so the green one is not the verdict"},
		{id: "PR-294", want: "", why: "an EXPECTED StatusContext is a required check nobody has reported: not a pass"},
		{id: "PR-293", want: "", why: "a STALE result beside a SUCCESS is not a passing suite"},
		{id: "PR-292", want: "", why: "STARTUP_FAILURE never ran, so it is neither a pass nor a measured failure"},
	}

	for _, tc := range cases {
		spec := specByID(t, specs, tc.id)
		red, green := spec.HasLabel(LabelCheckRed), spec.HasLabel(LabelCheckGreen)
		if red && green {
			t.Errorf("%s is both red and green", tc.id)
		}
		got := ""
		if red {
			got = LabelCheckRed
		} else if green {
			got = LabelCheckGreen
		}
		if got != tc.want {
			t.Errorf("%s check verdict = %q, want %q: %s", tc.id, got, tc.want, tc.why)
		}
	}
}

func verdictName(v checkVerdict) string {
	switch v {
	case checksAbsent:
		return "absent"
	case checksRed:
		return "red"
	case checksPending:
		return "pending"
	case checksGreen:
		return "green"
	}
	return "unknown"
}

// Every rollup value GitHub can send, one row each, against the only rule that
// is safe: green is minted for an explicit SUCCESS and for nothing else.
//
// The fallback this replaced — "not red, so green" — passes nine of these rows
// as green, and each one is a pull request the quiet timer would then advance
// past a check that had not passed: a required context still EXPECTED, a
// result already STALE, a runner that died in STARTUP_FAILURE, a conclusion
// GitHub has not invented yet.
func TestOnlyAnExplicitSuccessIsGreen(t *testing.T) {
	cases := []struct {
		name  string
		entry checkEntry
		want  checkVerdict
	}{
		{"CheckRun SUCCESS", checkEntry{Conclusion: "SUCCESS", Status: "COMPLETED"}, checksGreen},
		{"StatusContext SUCCESS", checkEntry{State: "SUCCESS"}, checksGreen},
		// Compared upper-cased, so a spelling GitHub does not currently
		// send still reads as the pass it is rather than as unknown.
		{"success in another case", checkEntry{Conclusion: "success", Status: "completed"}, checksGreen},

		{"CheckRun FAILURE", checkEntry{Conclusion: "FAILURE", Status: "COMPLETED"}, checksRed},
		{"CheckRun TIMED_OUT", checkEntry{Conclusion: "TIMED_OUT", Status: "COMPLETED"}, checksRed},
		{"CheckRun CANCELLED", checkEntry{Conclusion: "CANCELLED", Status: "COMPLETED"}, checksRed},
		{"CheckRun ACTION_REQUIRED", checkEntry{Conclusion: "ACTION_REQUIRED", Status: "COMPLETED"}, checksRed},
		{"StatusContext FAILURE", checkEntry{State: "FAILURE"}, checksRed},
		{"StatusContext ERROR", checkEntry{State: "ERROR"}, checksRed},

		{"StatusContext EXPECTED", checkEntry{State: "EXPECTED"}, checksPending},
		{"StatusContext PENDING", checkEntry{State: "PENDING"}, checksPending},
		{"CheckRun STALE", checkEntry{Conclusion: "STALE", Status: "COMPLETED"}, checksPending},
		{"CheckRun STARTUP_FAILURE", checkEntry{Conclusion: "STARTUP_FAILURE", Status: "COMPLETED"}, checksPending},
		{"CheckRun NEUTRAL", checkEntry{Conclusion: "NEUTRAL", Status: "COMPLETED"}, checksPending},
		{"CheckRun SKIPPED", checkEntry{Conclusion: "SKIPPED", Status: "COMPLETED"}, checksPending},
		{"CheckRun QUEUED", checkEntry{Status: "QUEUED"}, checksPending},
		{"CheckRun IN_PROGRESS", checkEntry{Status: "IN_PROGRESS"}, checksPending},
		{"a conclusion GitHub adds tomorrow", checkEntry{Conclusion: "PARTIALLY_SUCCEEDED", Status: "COMPLETED"}, checksPending},
		{"an entry that says nothing at all", checkEntry{}, checksPending},
	}

	for _, tc := range cases {
		if got := tc.entry.verdict(); got != tc.want {
			t.Errorf("%s: verdict = %s, want %s", tc.name, verdictName(got), verdictName(tc.want))
		}
		// The same value through the reducer: one entry is the whole
		// rollup there, so a reducer that stopped consulting the
		// classifier fails here as well.
		if got := checksOf([]checkEntry{tc.entry}); got != tc.want {
			t.Errorf("%s: checksOf = %s, want %s", tc.name, verdictName(got), verdictName(tc.want))
		}
	}

	// Order must not decide the verdict, or a rollup would read green on
	// the luck of how GitHub happened to sort it.
	green := checkEntry{Conclusion: "SUCCESS", Status: "COMPLETED"}
	stale := checkEntry{Conclusion: "STALE", Status: "COMPLETED"}
	for _, order := range [][]checkEntry{{green, stale}, {stale, green}} {
		if got := checksOf(order); got != checksPending {
			t.Errorf("a SUCCESS beside a STALE read %s, want pending whichever arrives first", verdictName(got))
		}
	}
	// An empty rollup is absence, not a verdict: nobody looked.
	if got := checksOf(nil); got != checksAbsent {
		t.Errorf("an empty rollup read %s, want absent", verdictName(got))
	}
}

// GitHub serves two shapes on statusCheckRollup and they share no field name.
// Neither the CheckRun-only decode nor a bare `status != "COMPLETED"` survives
// this: the first reads a failing repository as having no checks, the second
// reads a passing one as forever pending, because a StatusContext has no
// `status` key at all.
func TestLegacyStatusContextIsDecodedRatherThanIgnored(t *testing.T) {
	s, _ := stubSource(t, activityRepo, 0, loadFixture(t, "pr_list_activity.json"), nil)
	s.WithActivity()
	specs, _ := s.Load(context.Background())
	if got := specByID(t, specs, "PR-296"); !got.HasLabel(LabelCheckRed) {
		t.Errorf("a StatusContext in FAILURE produced %v, want %q", got.Labels, LabelCheckRed)
	}

	// The same shape passing must read green rather than pending.
	payload := []byte(`[{"number":296,"state":"OPEN","createdAt":"2026-09-17T06:40:00Z",` +
		`"updatedAt":"2026-09-17T07:00:12Z","title":"legacy","author":{"login":"aryrabelo","is_bot":false},` +
		`"statusCheckRollup":[{"__typename":"StatusContext","context":"buildkite/gates",` +
		`"state":"SUCCESS","createdAt":"2026-09-17T07:00:00Z"}]}]`)
	green, _ := stubSource(t, activityRepo, 0, payload, nil)
	green.WithActivity()
	specs, errs := green.Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	card := specByID(t, specs, "PR-296")
	if !card.HasLabel(LabelCheckGreen) {
		t.Errorf("a StatusContext in SUCCESS produced %v, want %q: an absent `status` key is absence, not pending", card.Labels, LabelCheckGreen)
	}
	if got, _ := labelWithPrefix(card, LabelActivityPrefix); got != "2026-09-17T07:00:00Z" {
		t.Errorf("activity = %q, want the StatusContext's createdAt", got)
	}
}

// The three facts the production line reads are exactly the three worth
// forging: a repository label could otherwise declare an open pull request
// merged (skipping every column), green (skipping the check gate), and last
// active in 2020 (expiring the quiet timer immediately). The fixture is one
// open pull request wearing all three.
func TestRepositoryCannotForgeStateChecksOrActivity(t *testing.T) {
	s, _ := stubSource(t, activityRepo, 0, loadFixture(t, "pr_list_forged_state.json"), nil)
	s.WithActivity()
	specs, errs := s.Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	card := specByID(t, specs, "PR-9299")

	if card.HasLabel(LabelMerged) {
		t.Error("a repository label forged hvb:state:merged: this open pull request would land in the merged column")
	}
	if got := stateLabels(card); !equalStrings(got, []string{LabelOpen}) {
		t.Errorf("state labels = %v, want %v: the measured fact is the only one that may survive", got, []string{LabelOpen})
	}
	if card.HasLabel(LabelCheckGreen) {
		t.Error("a repository label forged hvb:check:green: the line would skip the check gate")
	}
	if got, ok := labelWithPrefix(card, LabelActivityPrefix); ok {
		t.Errorf("a repository label forged activity %q: the quiet timer would expire on an unmeasured pull request", got)
	}
	if !card.HasLabel("needs-review") {
		t.Errorf("labels = %v, want the repository's own \"needs-review\" kept", card.Labels)
	}
}

// The same three forgeries with Shift held down, and one padded with spaces.
// The discard used to be a byte-exact prefix test, so `hvb:state:merged` was
// dropped and `HVB:STATE:MERGED` was carried — yet every consumer of these
// facts folds case after trimming, so the uppercase spelling forged exactly
// the same verdict. GitHub allows both the case and the padding in a label
// name, which makes this the cheapest forgery available to anyone with write
// access to the repository's labels.
//
// The assertions are the card's whole label list and a scan by the tests' own
// claimsPrefix, not HasLabel and not the production predicate: HasLabel
// compares bytes, so it cannot see this forgery at all, and borrowing the
// predicate under test would let one mutation silence both sides.
func TestReservedPrefixIsDroppedWhateverItsCaseOrPadding(t *testing.T) {
	s, _ := stubSource(t, activityRepo, 0, loadFixture(t, "pr_list_forged_state.json"), nil)
	s.WithActivity()
	specs, errs := s.Load(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	card := specByID(t, specs, "PR-9298")

	// Any label claiming the reserved namespace has to be one this package
	// minted on this very card. Naming the four rather than the forgeries
	// is what keeps the test from going stale: a fifth spelling of the
	// forgery in the fixture needs no new assertion.
	minted := map[string]bool{
		LabelSourcePR:                    true,
		LabelPrefix + "pr:9298":          true,
		LabelPrefix + "author:aryrabelo": true,
		LabelOpen:                        true,
	}
	for _, label := range card.Labels {
		if claimsPrefix(label) && !minted[label] {
			t.Errorf("label %q claims the reserved namespace and this package never minted it: the repository's forgery survived the discard, so this open pull request reads as merged, green and quiet to any consumer that folds case", label)
		}
	}

	// Stated positively too: the card carries exactly what this package
	// measured plus the repository's one honest label.
	want := []string{LabelSourcePR, LabelPrefix + "pr:9298", LabelPrefix + "author:aryrabelo", LabelOpen, "needs-review"}
	if !equalStrings(card.Labels, want) {
		t.Errorf("labels = %v, want %v", card.Labels, want)
	}
	if got := stateLabels(card); !equalStrings(got, []string{LabelOpen}) {
		t.Errorf("state labels = %v, want %v", got, []string{LabelOpen})
	}
}
