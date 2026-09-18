// Package usinasrc reads the owner's real queue: the issues of a CEO
// repository, routed by the `usina` CLI when it answers and served by `gh`
// when it does not.
//
// Two external binaries, one injected seam. Load asks `usina rota show` which
// repository actually owns the issues, then `usina issue list` for the cards.
// Silent degradation is an amplifier, so every answer carries who produced it:
// Origin.Source names the binary that served the list, and Origin.Reason is
// empty only when the preferred source answered in full — route included. Any
// other path names what failed, inside the returned struct rather than in a
// log line nobody reads. Nothing here ever returns an empty queue without
// saying why.
//
// A `recusa` is data, not a failure. The route answers one object per artefact
// area, and an area the owner never configured comes back carrying the phrase
// to declare; the queue is read anyway, because a refused sibling area says
// nothing about the issues. Only the `issues` area's own phrase reaches
// Queue.RouteRefusal — see that field for why a sibling's never does.
//
// Absence is absence. A card is built only from fields the source measured: an
// issue with no recognisable state or number costs the list rather than
// becoming an open issue nobody reported on, since Closed false is
// indistinguishable from a measured OPEN.
package usinasrc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner invokes an external binary and returns its stdout. It is the single
// seam between this package and the outside world, so tests replace it with a
// fixture reader and never execute usina or gh.
type Runner func(name string, args ...string) ([]byte, error)

// Card is one issue of the owner's queue. Every field is measured: Labels and
// Assignees are always non-nil, so a card with no assignee is an empty slice a
// consumer can range over, never a nil the board has to guard.
type Card struct {
	Number    int
	Title     string
	URL       string
	Closed    bool
	Labels    []string
	Assignees []string
	// UpdatedAt is RFC3339 when the answering binary sent one and empty
	// otherwise: usina's CAMPOS_LISTA does not include it (measured
	// 2026-09-17), gh does when asked. The board shows no age rather than
	// a made-up one.
	UpdatedAt string
}

// Origin says who answered and, when the preferred source did not, why.
//
// Source is the binary that served the list: SourceUsina or SourceGH, and
// empty only when neither answered — a state Load reports with an error, never
// with an innocent-looking zero value. Reason is empty exclusively when usina
// served both the route and the list; every other path names the failure.
type Origin struct {
	Source string
	Reason string
}

// Queue is the owner's queue as one source answered it.
//
// Repo is the repository the route resolved as the owner of the issues, which
// is the CEO repository itself when the route could not name one — and then
// Origin.Reason says so. RouteRefusal carries the `recusa` phrase of the
// route's `issues` area verbatim, and is empty when that area refused nothing
// — including when a sibling area did.
//
// Per area, because a sibling's refusal is not about this queue and reporting
// it is a false alarm: measured 2026-09-17 against aryrabelo/ceo-bora, `kb` is
// absent from the usina-config, so EVERY route of that CEO refuses `kb`, and
// the kanban never reads `kb` (that is the team's KB). Joining the areas lit a
// permanent problem on the board for an area it does not even display.
type Queue struct {
	Repo         string
	Cards        []Card
	Origin       Origin
	RouteRefusal string
}

// The two binaries this package shells out to, and the only values Load ever
// puts in Origin.Source.
const (
	SourceUsina = "usina"
	SourceGH    = "gh"
)

// ghFields is the exact field set the fallback asks gh for. The names match
// what this package decodes from usina, which is why one decoder serves both.
const ghFields = "number,title,url,state,labels,assignees,updatedAt"

// Load reads label's issues of ceoRepo's queue, at most limit of them.
//
// The route decides which repository is actually asked; a dead route falls
// back to ceoRepo with the failure named in Origin.Reason. A usina list that
// does not answer degrades to gh, again named. An error is returned only when
// the call is mis-wired — no runner, no repo, no limit — or when neither
// binary answered; in that last case the Queue still carries the route's
// findings, so the caller can show what was learned before everything failed.
func Load(run Runner, ceoRepo, label string, limit int) (Queue, error) {
	ceoRepo = strings.TrimSpace(ceoRepo)
	switch {
	case run == nil:
		return Queue{}, errors.New("usinasrc: nenhum Runner injetado")
	case ceoRepo == "":
		return Queue{}, errors.New("usinasrc: repo do CEO vazio")
	case limit <= 0:
		return Queue{}, fmt.Errorf("usinasrc: limite tem de ser positivo, veio %d", limit)
	}

	route := resolveRoute(run, ceoRepo)
	queue := Queue{
		Repo:         route.repo,
		RouteRefusal: route.refusal,
		Origin:       Origin{Source: SourceUsina, Reason: route.reason},
	}

	cards, usinaErr := listViaUsina(run, route.repo, label, limit)
	if usinaErr == nil {
		queue.Cards = cards
		return queue, nil
	}

	cards, ghErr := listViaGH(run, route.repo, label, limit)
	if ghErr != nil {
		queue.Origin = Origin{Reason: joinReasons(route.reason, fmt.Sprintf(
			"usinasrc: nenhuma fonte listou %s: usina: %v; gh: %v", route.repo, usinaErr, ghErr))}
		return queue, fmt.Errorf("usinasrc: nenhuma fonte listou as issues de %s: usina: %w; gh: %v",
			route.repo, usinaErr, ghErr)
	}

	queue.Cards = cards
	queue.Origin = Origin{Source: SourceGH, Reason: joinReasons(route.reason, fmt.Sprintf(
		"usinasrc: usina issue list nao respondeu (%v): fila de %s lida por gh", usinaErr, route.repo))}
	return queue, nil
}

// route is what `usina rota show` taught us: which repository owns the issues,
// the phrases it refused, and — when it taught us nothing — why not.
type route struct {
	repo    string
	refusal string
	reason  string
}

// resolveRoute asks the route who owns ceoRepo's issues. It never fails: a
// missing binary, a non-zero exit, unparseable stdout, or an answer that never
// names an issues repository all resolve to ceoRepo plus a reason, because the
// queue is still readable from the CEO repository itself.
func resolveRoute(run Runner, ceoRepo string) route {
	stdout, err := run(SourceUsina, "rota", "show", "--repo", ceoRepo)
	if err != nil {
		return route{repo: ceoRepo, reason: fmt.Sprintf(
			"usinasrc: usina rota show nao respondeu (%v): fila lida do proprio %s", err, ceoRepo)}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &fields); err != nil {
		return route{repo: ceoRepo, reason: fmt.Sprintf(
			"usinasrc: saida de usina rota show nao e JSON (%v): fila lida do proprio %s", err, ceoRepo)}
	}

	refusal := refusalFor(fields, areaIssues)
	repo, ok := issuesRepo(fields)
	if !ok {
		return route{repo: ceoRepo, refusal: refusal, reason: fmt.Sprintf(
			"usinasrc: usina rota show nao nomeou o repo de 'issues': fila lida do proprio %s", ceoRepo)}
	}
	return route{repo: repo, refusal: refusal}
}

// areaIssues is the route's key this package cares about: the artefact area
// that names the repository the queue lives in, and the only one whose
// `recusa` is about the queue.
const areaIssues = "issues"

// issuesRepo reads the route's `issues` key, which is the repository the queue
// lives in. The key is absent, or an object rather than a string, exactly when
// the route has nothing to say about issues; both are "not measured" here.
func issuesRepo(fields map[string]json.RawMessage) (string, bool) {
	raw, ok := fields[areaIssues]
	if !ok {
		return "", false
	}
	var repo string
	if err := json.Unmarshal(raw, &repo); err != nil {
		return "", false
	}
	repo = strings.TrimSpace(repo)
	return repo, repo != ""
}

// refusalFor reads one area's own `recusa` phrase, verbatim. It is empty when
// the area is absent, when it answered with a repository instead of a refusal,
// or when it refused nothing — all three being "this area has no complaint
// about itself", and none of them a statement about any sibling area.
func refusalFor(fields map[string]json.RawMessage, area string) string {
	raw, ok := fields[area]
	if !ok {
		return ""
	}
	var decoded struct {
		Recusa string `json:"recusa"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ""
	}
	return strings.TrimSpace(decoded.Recusa)
}

// listViaUsina asks the preferred source for the cards.
//
// --state all is not optional, and its absence was a real defect: measured
// 2026-09-17 against aryrabelo/ceo-bora, `usina issue list` without it returns
// 30 issues (OPEN only) while the same call with it returns 32 (30 OPEN +
// 2 CLOSED). Since the gh degradation forces --state all, omitting it here
// makes the board's Done column depend on WHICH source answered -- the exact
// disagreement a declared degradation exists to prevent, and one that reads as
// "the closed cards vanished" rather than as a source switch.
func listViaUsina(run Runner, repo, label string, limit int) ([]Card, error) {
	args := []string{"issue", "list", "--repo", repo}
	if label = strings.TrimSpace(label); label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, "--state", "all", "--limit", strconv.Itoa(limit))

	stdout, err := run(SourceUsina, args...)
	if err != nil {
		return nil, err
	}
	return decodeCards(SourceUsina, stdout)
}

// listViaGH is the declared degradation: the same question asked of gh, which
// speaks the same field names. --state all keeps a closed issue visible, since
// a card's Closed flag is part of what the board renders.
func listViaGH(run Runner, repo, label string, limit int) ([]Card, error) {
	args := []string{"issue", "list", "--repo", repo}
	if label = strings.TrimSpace(label); label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, "--state", "all", "--limit", strconv.Itoa(limit), "--json", ghFields)

	stdout, err := run(SourceGH, args...)
	if err != nil {
		return nil, err
	}
	return decodeCards(SourceGH, stdout)
}

// issue mirrors one element of the JSON array both binaries print. Unlisted
// keys are ignored, so either CLI growing its payload cannot break the parse.
type issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	UpdatedAt string `json:"updatedAt"`
}

// decodeCards turns one source's array into cards. source names the binary in
// every error, so a caller degrading from usina to gh can say which one lied.
func decodeCards(source string, stdout []byte) ([]Card, error) {
	var issues []issue
	if err := json.Unmarshal(stdout, &issues); err != nil {
		return nil, fmt.Errorf("saida de %s nao e um array JSON de issues: %w", source, err)
	}

	cards := make([]Card, 0, len(issues))
	for position, raw := range issues {
		if raw.Number <= 0 {
			return nil, fmt.Errorf("%s: issue na posicao %d veio sem numero", source, position)
		}
		closed, err := closedFromState(raw.State)
		if err != nil {
			return nil, fmt.Errorf("%s: issue %d: %w", source, raw.Number, err)
		}

		labels := make([]string, 0, len(raw.Labels))
		for _, label := range raw.Labels {
			if name := strings.TrimSpace(label.Name); name != "" {
				labels = append(labels, name)
			}
		}
		assignees := make([]string, 0, len(raw.Assignees))
		for _, assignee := range raw.Assignees {
			if login := strings.TrimSpace(assignee.Login); login != "" {
				assignees = append(assignees, login)
			}
		}

		cards = append(cards, Card{
			Number:    raw.Number,
			Title:     raw.Title,
			URL:       raw.URL,
			Closed:    closed,
			Labels:    labels,
			Assignees: assignees,
			UpdatedAt: strings.TrimSpace(raw.UpdatedAt),
		})
	}
	return cards, nil
}

// closedFromState reads the lifecycle both binaries report as OPEN or CLOSED,
// in whatever case they feel like. An unrecognised or missing state is an
// error rather than an open card: Closed false would be a measurement this
// source never made.
func closedFromState(state string) (bool, error) {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "CLOSED":
		return true, nil
	case "OPEN":
		return false, nil
	case "":
		return false, errors.New("sem 'state'")
	default:
		return false, fmt.Errorf("'state' desconhecido %q", state)
	}
}

// joinReasons keeps both halves of a degraded answer. The route's complaint
// and the list's complaint are different facts; dropping either would hide who
// failed first.
func joinReasons(reasons ...string) string {
	kept := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			kept = append(kept, reason)
		}
	}
	return strings.Join(kept, "; ")
}

// ExecRunner is the production Runner, running the children in the current
// working directory.
//
// Authentication and configuration are the child process's problem — usina
// reads its own instance yml, gh its own keyring — so nothing secret passes
// through here, and a nil Stdin guarantees neither binary can block this call
// waiting for input it will never get.
func ExecRunner(name string, args ...string) ([]byte, error) {
	return ExecRunnerIn("", name, args...)
}

// ExecRunnerIn is ExecRunner with an explicit working directory, which usina
// needs: it resolves which instance it is talking about from the directory it
// runs in, and refuses outright from anywhere else. An empty dir means the
// current directory, so ExecRunner is this function with nothing to say.
func ExecRunnerIn(dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s nao esta no PATH: %w", name, err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil

	if err := cmd.Run(); err != nil {
		return nil, runError(name, execTimeout, ctx.Err(), err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

// runError keeps the child's own diagnosis. usina prints its refusal to stderr
// and exits 2 ("instancia da usina indeterminada: nenhum segmento 'ceo-<nome>'
// no cwd", measured); gh prints "not logged in" the same way. That text is the
// useful part — the exit status alone is not — and it is what reaches
// Origin.Reason, which is why it is bounded here.
//
// The deadline is the one failure with no diagnosis to keep: the context kills
// the child, so stderr is empty and ExitCode() is -1, and forwarding that puts
// "usina saiu -1: signal: killed" on the board — a Reason naming neither the
// deadline nor its value. ctxErr is the only witness that the kill was ours,
// so it is read first; a child that exited on its own keeps its own text.
func runError(name string, timeout time.Duration, ctxErr, runErr error, stderr []byte) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return fmt.Errorf("%s passou do prazo de %s e foi interrompido antes de responder", name, timeout)
	}

	reason := strings.TrimSpace(string(stderr))
	if reason == "" {
		reason = runErr.Error()
	}
	if len(reason) > maxStderrBytes {
		reason = reason[:maxStderrBytes] + "…"
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return fmt.Errorf("%s saiu %d: %s", name, exitErr.ExitCode(), reason)
	}
	return fmt.Errorf("%s nao pode ser executado: %s", name, reason)
}

const (
	// execTimeout bounds one child call. `usina issue list` makes its own gh
	// requests and took 4.1s for three issues (measured), so its cost grows
	// with the project the same way `kit.py fronteira` does — 69s for 33 open
	// cards there (measured) — and 60s was already inside reach of a project
	// this size. 3 minutes keeps the same headroom as fila.execTimeout, and
	// stays bounded so a stuck refresh reports instead of hanging forever.
	execTimeout = 3 * time.Minute
	// maxStderrBytes caps how much of the child's stderr reaches a Reason the
	// board has to render.
	maxStderrBytes = 512
)
