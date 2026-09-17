// Package ghboard reads a GitHub repository's recent pull requests and
// presents them as feature specs, so the board can show real shipped and
// cancelled work next to the owner's own queue.
//
// It is strictly read-only: it shells out to the already-authenticated `gh`
// binary and never writes to GitHub, never opens an HTTP connection of its own,
// and never handles a token. A broken source degrades to zero cards plus a
// visible error; it never panics and never aborts the batch.
package ghboard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// Source loads pull requests from one repository.
type Source struct {
	repo   string
	window time.Duration

	// run is the gh seam; tests swap it for a fixture reader.
	run runner
	// now is the clock the window is measured against; tests pin it.
	now func() time.Time
	// timeout bounds a single gh invocation.
	timeout time.Duration
	// limit is how many pull requests gh is asked for.
	limit int
}

// New returns a Source for repo in `owner/name` form. window is the age of the
// slice of history the board shows: a pull request qualifies when it was
// opened, merged, or closed inside it. A window of zero or less means no age
// filter at all.
func New(repo string, window time.Duration) *Source {
	return &Source{
		repo:    strings.TrimSpace(repo),
		window:  window,
		run:     execRunner,
		now:     time.Now,
		timeout: defaultTimeout,
		limit:   defaultLimit,
	}
}

const (
	// defaultTimeout bounds one `gh pr list` call. The GraphQL query behind
	// closingIssuesReferences takes seconds on a busy repo; a board refresh
	// that hangs past this is a failure worth reporting.
	defaultTimeout = 30 * time.Second
	// defaultLimit is how deep into pull-request history gh is asked to
	// look. gh's own default of 30 is too shallow for a busy week, and the
	// window filter discards whatever is too old.
	defaultLimit = 200
	// maxBodyBytes caps the body carried into a card. The detail pane shows
	// the opening of a description, not a whole design document.
	maxBodyBytes = 4096
	// truncationMarker terminates a body that was cut.
	truncationMarker = "\n\n… (truncated)"
)

// ghFields is the exact field set measured against gh 2.97.0. Every name here
// was confirmed by `gh pr list --json` rejecting an unknown field and printing
// its catalogue; closingIssuesReferences is served by that same call, which is
// why this package needs no separate `gh api graphql` round trip.
const ghFields = "number,title,author,state,isDraft,mergedAt,closedAt," +
	"createdAt,updatedAt,labels,url,headRefName,body,isCrossRepository," +
	"closingIssuesReferences"

// pullRequest mirrors the JSON gh emits for the fields above. Unlisted keys are
// ignored, so gh growing its payload cannot break the parse.
type pullRequest struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	URL       string `json:"url"`
	HeadRef   string `json:"headRefName"`
	Body      string `json:"body"`
	IsDraft   bool   `json:"isDraft"`
	CrossRepo bool   `json:"isCrossRepository"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	MergedAt  string `json:"mergedAt"`
	ClosedAt  string `json:"closedAt"`
	Author    struct {
		Login string `json:"login"`
		IsBot bool   `json:"is_bot"`
	} `json:"author"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	ClosingIssues []struct {
		Number     int `json:"number"`
		Repository struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	} `json:"closingIssuesReferences"`
}

// Load fetches the window's pull requests and converts them to specs. Errors
// are returned alongside whatever was parsed successfully: one unreadable pull
// request costs its own card, not the batch.
func (s *Source) Load(ctx context.Context) ([]*feature.Spec, []error) {
	owner, _, ok := splitRepo(s.repo)
	if !ok {
		return nil, []error{fmt.Errorf("ghboard: repository %q is not in owner/name form", s.repo)}
	}

	raw, err := s.fetch(ctx)
	if err != nil {
		return nil, []error{fmt.Errorf("ghboard: %s: %w", s.repo, err)}
	}

	var prs []*pullRequest
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, []error{fmt.Errorf("ghboard: %s: gh returned unparseable JSON: %w", s.repo, err)}
	}
	// Newest first: the board's terminal columns are read top-down as "what
	// just happened". Ordering the input rather than the output keeps the
	// pull-request number an int throughout; nothing has to re-parse a card
	// id to know which card is newer.
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })

	cutoff := time.Time{}
	if s.window > 0 {
		cutoff = s.now().Add(-s.window)
	}

	specs := make([]*feature.Spec, 0, len(prs))
	var errs []error
	for _, pr := range prs {
		if pr.Number <= 0 {
			errs = append(errs, fmt.Errorf("ghboard: %s: pull request entry with no number (title %q) skipped", s.repo, pr.Title))
			continue
		}
		touched, err := pr.touchedAt()
		if err != nil {
			errs = append(errs, fmt.Errorf("ghboard: %s#%d: %w", s.repo, pr.Number, err))
			continue
		}
		if !cutoff.IsZero() && touched.Before(cutoff) {
			continue
		}
		specs = append(specs, pr.spec(owner))
	}

	return specs, errs
}

func (s *Source) fetch(ctx context.Context) ([]byte, error) {
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	return s.run(ctx,
		"pr", "list",
		"--repo", s.repo,
		"--state", "all",
		"--limit", strconv.Itoa(s.limit),
		"--json", ghFields,
	)
}

// touchedAt is the most recent moment the pull request entered or left the
// board: its merge, else its close, else its creation. The window is measured
// against this so a pull request opened long ago but merged this week still
// counts as this week's work.
func (p *pullRequest) touchedAt() (time.Time, error) {
	candidates := []struct {
		field string
		raw   string
	}{
		{"mergedAt", p.MergedAt},
		{"closedAt", p.ClosedAt},
		{"createdAt", p.CreatedAt},
	}
	var newest time.Time
	var parsed bool
	for _, c := range candidates {
		if c.raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, c.raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s is not an RFC3339 timestamp: %q", c.field, c.raw)
		}
		parsed = true
		if t.After(newest) {
			newest = t
		}
	}
	if !parsed {
		return time.Time{}, fmt.Errorf("no usable timestamp: createdAt, mergedAt and closedAt are all empty")
	}
	return newest, nil
}

// spec converts one pull request to a card. repoOwner is the repository owner,
// used to tell a local linked issue from a cross-repository one.
func (p *pullRequest) spec(repoOwner string) *feature.Spec {
	author := normalizeAuthor(p.Author.Login, p.Author.IsBot)
	status, canceled := p.status()

	labels := newLabelSet(4 + len(p.Labels) + len(p.ClosingIssues))
	labels.add(LabelSourcePR)
	labels.add(LabelPrefix + "pr:" + strconv.Itoa(p.Number))
	if author != "" {
		labels.add(LabelPrefix + "author:" + author)
	}
	if canceled {
		labels.add(LabelCanceled)
	}
	// External means "this did not come from a teammate", and it is decided
	// without a second network call, from two fields already in hand.
	//
	// A fork is outside by construction. A bot or GitHub App is automation
	// rather than a teammate even when it has push access — which is the
	// case the design hangs on: aryrabelo/cnb_landing_page#179 is badged
	// External in the approved print, yet isCrossRepository is false there,
	// because factory-bora pushes its branch into the repository itself.
	//
	// The remaining case needs no signal at all: pushing a branch into the
	// repository requires push access, so a human author who did not use a
	// fork is necessarily a collaborator or member, i.e. a teammate.
	//
	// Measured against the two anchors. #179: is_bot=true → External, so
	// the print is reproduced. cli/cli: williammartin, niik and babakks
	// come back internal while vimalyad (fork) and app/dependabot (bot) do
	// not, so an organisation repository no longer badges every card — the
	// earlier owner-mismatch rule returned External for 15 of 15 there.
	//
	// What it still gets wrong: an organisation member who works from their
	// own fork reads External. GitHub's own authorAssociation would say
	// MEMBER, but it is not obtainable cheaply — `gh pr list --json` does
	// not offer it (46 fields, measured) and the REST route costs one call
	// per pull request.
	if p.CrossRepo || p.Author.IsBot {
		labels.add(LabelExternal)
	}
	if p.IsDraft {
		labels.add(LabelDraft)
	}

	deps := make([]string, 0, len(p.ClosingIssues))
	for _, issue := range p.ClosingIssues {
		if issue.Number <= 0 {
			continue
		}
		ref := issueRef(issue.Number, issue.Repository.Owner.Login, issue.Repository.Name, repoOwner)
		labels.add(LabelPrefix + "issue:" + ref)
		deps = append(deps, ref)
	}
	// Labels the repository put on the pull request pass through verbatim —
	// except any that claim the reserved prefix. Those are discarded, not
	// escaped and not renamed: a repository label is content anyone can
	// edit in the GitHub UI, so if `hvb:state:canceled` could arrive from
	// there it would forge a verdict the board treats as the harness's own.
	// Discarding is the only outcome that keeps a minted label a statement
	// about what hvb measured rather than about what a repository typed.
	for _, label := range p.Labels {
		if strings.HasPrefix(label.Name, LabelPrefix) {
			continue
		}
		labels.add(label.Name)
	}
	if len(deps) == 0 {
		deps = nil
	}

	return &feature.Spec{
		Frontmatter: feature.Frontmatter{
			ID:     "PR-" + strconv.Itoa(p.Number),
			Title:  strings.TrimSpace(p.Title),
			Status: status,
			Owner:  author,
			// RFC3339, exactly as gh emits it, so the card can render an
			// age without a second parse convention.
			Created:      p.CreatedAt,
			Updated:      p.UpdatedAt,
			Labels:       labels.slice(),
			Dependencies: deps,
		},
		// Path is the on-disk home of a spec. A pull request has none; the
		// browser URL lives in the body instead.
		Path: "",
		Body: prBody(p.URL, p.HeadRef, p.Body),
	}
}

// LabelPrefix marks a label this package minted, and is reserved: a label
// arriving from the repository that starts with it is dropped rather than
// carried. Without that, prefixing would only move the target — the forgery
// would just spell the prefix too.
const LabelPrefix = "hvb:"

// Labels every card from this source carries or may carry. The composing
// backend keys its extra terminal column off LabelCanceled and its badge off
// LabelExternal, so both are named here rather than spelled twice. Callers
// MUST use these constants; the strings are never typed out a second time.
const (
	LabelSourcePR = LabelPrefix + "source:pr"
	LabelCanceled = LabelPrefix + "state:canceled"
	LabelExternal = LabelPrefix + "external"
	LabelDraft    = LabelPrefix + "draft"
)

// status maps a pull request onto the five-state lifecycle without inventing a
// sixth. Merge is decided by mergedAt rather than by state, because gh reports
// closedAt for a merged pull request too (measured: #179 carries both, equal).
func (p *pullRequest) status() (status feature.Status, canceled bool) {
	switch {
	case p.MergedAt != "":
		return feature.Done, false
	case p.ClosedAt != "" || strings.EqualFold(p.State, "CLOSED"):
		// Closed without merging is terminal-but-not-shipped. The domain
		// has no `canceled`, so the fact travels as a label and the
		// composing board derives its own column from it.
		return feature.Done, true
	default:
		return feature.Review, false
	}
}

// normalizeAuthor renders a login the way GitHub's own UI does. gh reports a
// GitHub App as `app/factory-bora` with is_bot true; the board shows
// `factory-bora[bot]`, which is what the reviewer recognises.
func normalizeAuthor(login string, isBot bool) string {
	login = strings.TrimSpace(login)
	if login == "" {
		return ""
	}
	if !isBot || strings.HasSuffix(login, "[bot]") {
		return login
	}
	return strings.TrimPrefix(login, "app/") + "[bot]"
}

// issueRef renders a linked issue as a bare number when it lives in the same
// repository, and fully qualified when it does not, so a cross-repository link
// can never be mistaken for a local issue number.
func issueRef(number int, issueOwner, issueName, repoOwner string) string {
	n := strconv.Itoa(number)
	if issueName == "" || issueOwner == "" {
		return n
	}
	if strings.EqualFold(issueOwner, repoOwner) {
		return n
	}
	return issueOwner + "/" + issueName + "#" + n
}

// prBody is the detail-pane text: the browser URL and branch first, because
// those are the two things a reviewer acts on, then the description.
func prBody(url, headRef, body string) string {
	body = strings.ReplaceAll(strings.TrimSpace(body), "\r\n", "\n")
	body = truncate(body, maxBodyBytes)

	var b strings.Builder
	if url != "" {
		b.WriteString("URL: ")
		b.WriteString(url)
		b.WriteString("\n")
	}
	if headRef != "" {
		b.WriteString("Branch: ")
		b.WriteString(headRef)
		b.WriteString("\n")
	}
	if body != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(body)
	}
	return b.String()
}

// truncate cuts s to at most limit bytes without splitting a rune.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, " \n\t") + truncationMarker
}

// labelSet keeps labels in insertion order and drops duplicates, so a card's
// label list is a set and a consumer iterating it cannot render the same badge
// twice.
//
// It used to earn that by a collision in the data: cli/cli#13899 carries a
// repository label literally named `external`, which collided with the badge
// back when the badge was spelled the same. The reserved `hvb:` prefix removed
// that collision, so no pull request gh can return produces a duplicate any
// more. The guarantee now belongs to this type rather than to the pipeline —
// it is what makes `add` safe to call from two branches that both apply — and
// it is tested here rather than through a fixture that would have to be faked
// to exhibit a duplicate GitHub cannot emit.
type labelSet struct {
	seen  map[string]struct{}
	order []string
}

func newLabelSet(capacity int) *labelSet {
	return &labelSet{seen: make(map[string]struct{}, capacity), order: make([]string, 0, capacity)}
}

func (l *labelSet) add(label string) {
	label = strings.TrimSpace(label)
	if label == "" {
		return
	}
	if _, dup := l.seen[label]; dup {
		return
	}
	l.seen[label] = struct{}{}
	l.order = append(l.order, label)
}

func (l *labelSet) slice() []string {
	if len(l.order) == 0 {
		return nil
	}
	return l.order
}

func splitRepo(repo string) (owner, name string, ok bool) {
	owner, name, found := strings.Cut(repo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}
