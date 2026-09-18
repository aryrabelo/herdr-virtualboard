// Package linha decides which column of the declared production line a card
// belongs to. It is the whole column decision and nothing else: pure policy,
// no I/O, no package state, nothing to stub.
//
// The three authorities a card is pulled by are deliberately ranked rather than
// merged. A fact GitHub can prove (merged, closed, open) outranks anything hvb
// wrote down, because the alternative — letting a stored column hide a merged
// pull request — is the board lying about the repository. The store outranks the
// source's own guess, because the store is where a human's move is recorded. The
// source is the floor: a card with no fact and no stored move stays where the
// source put it.
package linha

import (
	"fmt"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// The labels this policy READS. The sources mint them (internal/ghboard,
// internal/issuesrc) and own the equivalent constants; they are respelled here
// instead of imported because a pure policy package that imports a source would
// drag the gh CLI, the process runner and their transitive state into every
// consumer that only wants to know where a card goes.
const (
	// LabelStateOpen is the one fact the quiet timer applies to: a pull
	// request that is open. Which column claims it is a configuration
	// matter (`when = "hvb:state:open"`), so the policy recognises the
	// label, not the column name.
	LabelStateOpen = "hvb:state:open"
	// LabelCheckRed means at least one check failed, which is a reason to
	// go back to building rather than forward to review.
	LabelCheckRed = "hvb:check:red"
	// LabelCheckGreen is the POSITIVE fact the quiet timer needs: every
	// check the forge reported concluded in success. The source mints it
	// only for an explicit success — never for a pending, expected or
	// absent check — because the timer's job is to advance a pull request
	// nobody needs to look at, and "nothing has run yet" is not that.
	LabelCheckGreen = "hvb:check:green"
	// LabelActivityPrefix carries the last MEASURED activity on the pull
	// request (a check or a comment) as RFC3339. It is not `updatedAt`: a
	// label edit bumps that without anyone touching the pull request.
	LabelActivityPrefix = "hvb:activity:"
)

// Policy is the declared line as data: the columns in order, the label each one
// claims as its own fact, and the column a quiet green pull request advances to.
type Policy struct {
	Order []feature.Status
	When  map[feature.Status]string
	Quiet feature.Status // "" when no column declared quiet = true
}

// Verdict is where a card goes and why.
type Verdict struct {
	Column feature.Status
	Reason string
	Pinned bool // a GitHub fact decided it; the store cannot override
}

// Input is everything the decision needs. It is a value, not a handle: the
// caller has already measured the labels, the store and the clock.
type Input struct {
	Policy   Policy
	Labels   []string
	Source   feature.Status // the column the source itself derived
	Stored   feature.Status // "" when the store holds nothing for this card
	Repo     string         // the repository the pull request lives in
	Timer    time.Duration
	TimerSet bool
	Now      time.Time
}

// Resolve decides a card's column from facts the card already carries, the
// stored override and the source's own guess.
//
// The precedence is fixed and total, and the order is the point:
//
//	first declared `when` label present -> that column, Pinned
//	  ... and it claims hvb:state:open   -> the quiet timer decides
//	  ... and Stored disagrees           -> the reason says so
//	Stored, when a declared column       -> Stored, not pinned
//	otherwise                            -> Source, no reason
//
// Scanning Policy.Order (and not the When map) is what makes the first rung
// deterministic: a pull request that is both merged and closed carries two
// matching labels, and the declared order is the only thing that says which
// fact wins.
func Resolve(in Input) Verdict {
	for _, column := range in.Policy.Order {
		label := strings.TrimSpace(in.Policy.When[column])
		if label == "" || !hasLabel(in.Labels, label) {
			continue
		}
		verdict := pinnedBy(in, column, label)
		// The store's disagreement is not discarded, it is reported: the
		// card detail has to be able to explain why a human's move is
		// not what the board shows.
		if in.Stored != "" && in.Stored != verdict.Column {
			verdict.Reason += "; o store dizia " + string(in.Stored)
		}
		return verdict
	}
	if in.Stored != "" && declares(in.Policy.Order, in.Stored) {
		return Verdict{Column: in.Stored, Reason: "coluna do store do hvb"}
	}
	// A stored column the line no longer declares is ignored rather than
	// rendered: dropping a column from the workflow would otherwise strand
	// every card a human had parked in it.
	return Verdict{Column: in.Source}
}

// pinnedBy builds the verdict for a card whose fact label matched, which is
// either the plain fact or — for an open pull request on a line that declared a
// quiet column — the timer's answer.
func pinnedBy(in Input, column feature.Status, label string) Verdict {
	if in.Policy.Quiet == "" || !strings.EqualFold(label, LabelStateOpen) {
		return Verdict{Column: column, Reason: "fato do GitHub: " + label, Pinned: true}
	}
	hold := func(reason string) Verdict {
		return Verdict{Column: column, Reason: reason, Pinned: true}
	}
	// No timer is not "no wait": a repository that never declared one has
	// not opted into having its pull requests advanced without a human, so
	// the card holds and the reason names the repository to configure.
	if !in.TimerSet {
		return hold("timer não configurado para " + in.Repo)
	}
	if hasLabel(in.Labels, LabelCheckRed) {
		return hold("check vermelho: retrocesso a building disponível")
	}
	// Green is REQUIRED, not merely "not red". A pull request whose checks
	// are still queued, whose forge reported none at all, or whose suite
	// never finished carries no verdict label either way — and advancing
	// it on the absence of a failure is how a board sends an untested
	// branch to review on a timer. The positive fact is the only thing
	// that means somebody's CI said yes.
	if !hasLabel(in.Labels, LabelCheckGreen) {
		return hold("sem check verde: o avanço exige sucesso medido, não ausência de vermelho")
	}
	activity, ok := ParseActivity(in.Labels)
	if !ok {
		// Absence of measurement is not silence: a pull request with no
		// check and no comment has nothing to be quiet about.
		return hold("sem atividade medida na PR")
	}
	quiet := in.Now.Sub(activity)
	timer := in.Timer.Round(time.Second)
	if quiet >= in.Timer {
		return Verdict{
			Column: in.Policy.Quiet,
			Reason: fmt.Sprintf("verde e quieto por %s (timer %s do repo %s)", quiet.Round(time.Second), timer, in.Repo),
			Pinned: true,
		}
	}
	return hold(fmt.Sprintf("quieto há %s, faltam %s de %s", quiet.Round(time.Second), (in.Timer - quiet).Round(time.Second), timer))
}

// ParseActivity reads the measured activity stamp off a card's labels.
func ParseActivity(labels []string) (time.Time, bool) {
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if len(label) <= len(LabelActivityPrefix) || !strings.EqualFold(label[:len(LabelActivityPrefix)], LabelActivityPrefix) {
			continue
		}
		when, err := time.Parse(time.RFC3339, label[len(LabelActivityPrefix):])
		if err != nil {
			// Degrade: an unreadable stamp is one the policy cannot use,
			// not a reason to drop a readable one further down the list.
			continue
		}
		return when, true
	}
	return time.Time{}, false
}

// declares reports whether the line still has this column.
func declares(order []feature.Status, column feature.Status) bool {
	for _, declared := range order {
		if declared == column {
			return true
		}
	}
	return false
}

// hasLabel compares the way a human reads a label: surrounding space and
// casing are noise, the name is the signal. Same rule as internal/fila.
func hasLabel(labels []string, want string) bool {
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label), want) {
			return true
		}
	}
	return false
}
