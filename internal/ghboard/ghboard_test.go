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
			labels: []string{LabelSourcePR, LabelPrefix + "pr:179", LabelPrefix + "author:factory-bora[bot]", LabelExternal, LabelPrefix + "issue:168", "status:auto-approved"},
			deps:   []string{"168"},
		},
		{
			// Merged by the repository owner: not External, no issue.
			id: "PR-176", status: feature.Done, owner: "aryrabelo",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:176", LabelPrefix + "author:aryrabelo"},
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
			labels: []string{LabelSourcePR, LabelPrefix + "pr:13899", LabelPrefix + "author:imkp1", LabelExternal, LabelPrefix + "issue:cli/cli#13804", "external", "gh-issue", "ready-for-review"},
			deps:   []string{"cli/cli#13804"},
		},
		{
			// Open draft: still Review, flagged so the card can say so.
			// Not External: a human pushing a branch into the
			// repository itself necessarily has push access, so this
			// is a maintainer even though the repository belongs to an
			// organisation nobody's login matches.
			id: "PR-14355", status: feature.Review, owner: "williammartin",
			labels: []string{LabelSourcePR, LabelPrefix + "pr:14355", LabelPrefix + "author:williammartin", LabelDraft},
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
			labels: []string{LabelSourcePR, LabelPrefix + "pr:2500", LabelPrefix + "author:zzz-yu", LabelExternal, LabelPrefix + "issue:spf13/cobra#706"},
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
	want := []string{LabelSourcePR, LabelPrefix + "pr:9176", LabelPrefix + "author:aryrabelo", "needs-review"}
	if !equalStrings(card.Labels, want) {
		t.Errorf("labels = %v, want %v", card.Labels, want)
	}
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
// half consults a list of names.
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
				if strings.HasPrefix(l.Name, LabelPrefix) {
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
				if strings.HasPrefix(label, LabelPrefix) {
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
