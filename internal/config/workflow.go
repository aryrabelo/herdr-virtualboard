package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// Workflow declares the line a sources board draws, left to right.
//
// It is deliberately absent from the spec board: `hvb tui` renders VirtualBoard
// spec markdown, and vb owns those five states. A declared line is for
// `hvb queue`, whose cards are pull requests, issues and threads that live
// outside any workspace.
type Workflow struct {
	// Columns are the column slugs in board order. Empty means the board
	// keeps the shape it had before this key existed; see BoardWorkflow.
	Columns []string `toml:"columns"`
}

// Repo is the policy for one repository, keyed by owner/name.
type Repo struct {
	// QuietTimer is how long a green pull request must go without a check
	// or a comment before the line advances it to the column declaring
	// `quiet = true`. There is no default: a repository that declares none
	// never advances on its own, because guessing a window here would move
	// someone's pull request forward on the board with nobody's say-so.
	QuietTimer Duration `toml:"quiet_timer"`
}

// The legacy tail. With no `[workflow] columns` declared, the sources board is
// the five vb states plus the cancelled column it used to derive for itself.
//
// These two halves are ONE decision and must move together. The column is what
// the board draws; the label is the only thing that can put a card in it. A
// synthesized column without its synthesized label is a column no card can
// reach, which silently leaves a cancelled pull request sitting in Done — the
// exact bug the derived column was added to fix.
const (
	legacyTailColumn = "canceled"
	legacyTailLabel  = "hvb:state:canceled"
)

// legacyTailDef is the column half of the pair above. HideWhenEmpty keeps it
// off a board that has nothing cancelled, which is every board over a
// VirtualBoard workspace.
func legacyTailDef() feature.ColumnDef {
	return feature.ColumnDef{Name: legacyTailColumn, HideWhenEmpty: true}
}

// BoardWorkflow is the workflow the sources board renders.
//
// The fallback is written here rather than in the TUI so both boards read the
// same answer from the same place: the renderer asks its backend for a
// workflow and draws whatever it gets.
func (c *Config) BoardWorkflow() (*feature.Workflow, error) {
	if len(c.Workflow.Columns) == 0 {
		return feature.NewWorkflow(append(vbDefs(), legacyTailDef()))
	}
	defs := make([]feature.ColumnDef, 0, len(c.Workflow.Columns))
	for _, name := range c.Workflow.Columns {
		column := c.Column(feature.Status(name))
		def := feature.ColumnDef{
			Name:      feature.Status(name),
			HumanGate: isHumanGate(column.Gate),
			Title:     column.Title,
			Short:     column.Short,
		}
		if len(column.Next) > 0 {
			def.Next = make([]feature.Status, 0, len(column.Next))
			for _, next := range column.Next {
				def.Next = append(def.Next, feature.Status(next))
			}
		}
		defs = append(defs, def)
	}
	return feature.NewWorkflow(defs)
}

// vbDefs re-declares the vb lifecycle as column definitions, read back off
// feature.VB() rather than spelled a second time here: a copy of the five
// states and their transitions in this package is a copy that can drift.
func vbDefs() []feature.ColumnDef {
	vb := feature.VB()
	columns := vb.Columns()
	defs := make([]feature.ColumnDef, 0, len(columns)+1)
	for _, name := range columns {
		defs = append(defs, feature.ColumnDef{
			Name:  name,
			Next:  vb.Next(name),
			Title: vb.Title(name),
			Short: vb.Short(name),
		})
	}
	return defs
}

// QuietTimer is the repository's declared quiet window. The bool is false when
// the repository declared none — never a silent default.
func (c *Config) QuietTimer(repo string) (time.Duration, bool) {
	entry, ok := c.Repos[repo]
	if !ok || entry.QuietTimer == 0 {
		return 0, false
	}
	return entry.QuietTimer.Duration(), true
}

// LineWhen is the `when` declaration the line policy reads: the label each
// column claims as its own fact, keyed by column name.
func (c *Config) LineWhen() map[string]string {
	when := map[string]string{}
	if len(c.Workflow.Columns) == 0 {
		when[legacyTailColumn] = legacyTailLabel
	}
	for name, column := range c.Columns {
		if label := strings.TrimSpace(column.When); label != "" {
			when[name] = label
		}
	}
	return when
}

// LineQuiet is the column declared `quiet = true`, or "" when none is. Validate
// allows at most one, and the names are walked in order so an unvalidated
// config still answers the same way twice.
func (c *Config) LineQuiet() string {
	for _, name := range c.columnNames() {
		if c.Columns[name].Quiet {
			return name
		}
	}
	return ""
}

// ValidateSpecBoard is the vb-authority gate: every column the spec board draws
// has to be a state vb can move a spec into, and every route a transition vb
// will accept.
//
// Load cannot ask this, which is why it is a separate method: `hvb queue`
// renders a declared line vb has never heard of, and refusing it at load would
// make a queue board impossible to configure. Nor can the resolver every
// workspace command shares, for the same reason one level down: `hvb role
// list` in a workspace configured for a queue board is not a spec board.
// `App.ResolveSpecBoard` — the `hvb tui` path — asks instead.
func (c *Config) ValidateSpecBoard() error {
	vb := feature.VB()
	for _, name := range c.columnNames() {
		status, ok := vb.Parse(name)
		if !ok {
			return fmt.Errorf("config: column %q is not a VirtualBoard status (vb is the authority on the spec board's lifecycle: %s). A column of your own is a `hvb queue` line, declared in [workflow] columns",
				name, joinColumns(vb.Columns()))
		}
		for _, route := range routesOf(c.Columns[name]) {
			target, ok := vb.Parse(route.target)
			if !ok {
				return fmt.Errorf("config: column %q %s: %q is not a VirtualBoard status (vb is the authority on the spec board's lifecycle: %s)",
					name, route.label, route.target, joinColumns(vb.Columns()))
			}
			if !vb.CanTransition(status, target) {
				return fmt.Errorf("config: column %q %s: vb does not allow %s → %s (vb is the authority on the spec board's lifecycle; from %s it allows: %s)",
					name, route.label, status, target, status, joinColumns(vb.Next(status)))
			}
		}
	}
	return nil
}

// quietTimerFloor and quietTimerCeiling bound a repository's quiet window.
//
// Both ends are measured against what the window is for. Below ten minutes the
// line advances a pull request while its checks are still being queued — a run
// that has not started yet has no activity to measure, so silence looks like
// calm. Above a day nobody is waiting for the board to do it: the pull request
// has been reviewed, merged or forgotten by hand long before.
const (
	quietTimerFloor   = 10 * time.Minute
	quietTimerCeiling = 24 * time.Hour
)

// validateQuietTimers keeps a declared window inside the bounds above.
func (c *Config) validateQuietTimers() error {
	names := make([]string, 0, len(c.Repos))
	for name := range c.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		window := c.Repos[name].QuietTimer.Duration()
		if window == 0 {
			continue
		}
		if window < quietTimerFloor || window > quietTimerCeiling {
			return fmt.Errorf("config: [repos.%q] quiet_timer %s is outside %s–%s (shorter advances a pull request whose checks have not started; longer is not a timer anyone waits for)",
				name, window, quietTimerFloor, quietTimerCeiling)
		}
	}
	return nil
}

// validateColumnGates checks the board-independent per-column keys: the gate is
// a person or nothing, at most one quiet destination, and every `next` entry
// names a column.
//
// An empty `next` entry is refused rather than ignored, because ignoring it is
// what made loading and rendering disagree: routesOf skips it, so Validate
// accepted `next = ["review", ""]`, and BoardWorkflow then handed
// feature.NewWorkflow a move to "" and the board refused to open at startup
// with a dangling-column error naming nothing. Unlike on_success, where ""
// means "no route" and is the documented way to clear an inherited one, a list
// entry has nothing to clear: it is a typo, and the place to say so is the
// file that has it.
func (c *Config) validateColumnGates() error {
	quiet := ""
	for _, name := range c.columnNames() {
		column := c.Columns[name]
		switch strings.ToLower(strings.TrimSpace(column.Gate)) {
		case "", "human":
		default:
			return fmt.Errorf("config: column %q gate %q is not \"human\" (the only gate is a person; omit the key for a column an agent may leave on its own)",
				name, column.Gate)
		}
		if column.Quiet {
			if quiet != "" {
				return fmt.Errorf("config: columns %q and %q both declare quiet = true (a quiet green pull request has exactly one destination)", quiet, name)
			}
			quiet = name
		}
		for index, next := range column.Next {
			if strings.TrimSpace(next) == "" {
				return fmt.Errorf(`config: column %q next[%d] is empty (every destination needs a column name; drop the entry, or omit next entirely for a column nothing moves off)`,
					name, index)
			}
		}
	}
	return nil
}

// columnSlug is the shape a declared column name may take: lower-case words
// joined by single hyphens. It is the spelling a `when` label, a directory and
// a flag value all survive, and `Ready To Review` as a name would produce a
// column nobody can type at a shell.
var columnSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validateDeclaredBoard checks a `[workflow] columns` line: the names are
// usable and unique, no hand-written `[columns.X]` table describes a column the
// line does not draw, and no column the line draws routes off it.
//
// Only tables a file wrote are held to the line. Default() ships the five vb
// columns, and declaring a line of your own replaces the board rather than
// contradicting it — refusing those would make `[workflow] columns` unusable.
func (c *Config) validateDeclaredBoard() error {
	declared := make(map[string]bool, len(c.Workflow.Columns))
	for index, name := range c.Workflow.Columns {
		switch {
		case strings.TrimSpace(name) == "":
			return fmt.Errorf(`config: workflow.columns[%d] is empty (every column needs a slug, e.g. "first-review")`, index)
		case !columnSlug.MatchString(name):
			return fmt.Errorf(`config: workflow.columns[%d] %q is not a slug (lower-case words joined by single hyphens, e.g. "ready-to-review")`, index, name)
		case declared[name]:
			return fmt.Errorf("config: workflow.columns lists %q twice (each column appears once, in board order)", name)
		}
		declared[name] = true
	}
	for _, name := range c.columnNames() {
		if !declared[name] {
			if c.fileColumns[name] {
				return fmt.Errorf("config: [columns.%s] describes a column workflow.columns does not draw (add %q to the list, or drop the table)", name, name)
			}
			// A built-in column the line does not draw is simply not
			// on this board, so its built-in routes are not either.
			continue
		}
		if !c.fileColumns[name] {
			// The same rule one step further in, and the reason it
			// has to be stated twice: a line is free to REUSE a
			// built-in name — "review", "done" — and inherit that
			// name's Default() table without having configured it.
			// Holding those inherited routes to the line refused a
			// workflow nobody wrote, over a move nobody declared,
			// and the only way out was to spell `on_failure = ""`
			// for a column the file never mentions.
			continue
		}
		for _, route := range routesOf(c.Columns[name]) {
			if !declared[route.target] {
				return fmt.Errorf("config: column %q %s: %q is not in workflow.columns (a card cannot move to a column the board does not draw; set %s = \"\" if the built-in pipeline put it there)",
					name, route.label, route.target, route.label)
			}
		}
	}
	return nil
}

// route is one declared destination of a column, with the key that declared it
// so an error can name the line the reader has to edit.
type route struct {
	label  string
	target string
}

// routesOf lists a column's destinations in a fixed order, so two runs over the
// same bad config name the same key.
func routesOf(column Column) []route {
	routes := make([]route, 0, len(column.Next)+2)
	if column.OnSuccess != "" {
		routes = append(routes, route{"on_success", column.OnSuccess})
	}
	if column.OnFailure != "" {
		routes = append(routes, route{"on_failure", column.OnFailure})
	}
	for _, next := range column.Next {
		if next != "" {
			routes = append(routes, route{"next", next})
		}
	}
	return routes
}

// columnNames lists the configured columns in a stable order. The map's own
// order is random, and an error message that names a different column on every
// run is one the reader cannot act on.
func (c *Config) columnNames() []string {
	names := make([]string, 0, len(c.Columns))
	for name := range c.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isHumanGate(gate string) bool {
	return strings.EqualFold(strings.TrimSpace(gate), "human")
}

func joinColumns(columns []feature.Status) string {
	if len(columns) == 0 {
		return "nothing — it is terminal"
	}
	names := make([]string, len(columns))
	for index, column := range columns {
		names[index] = string(column)
	}
	return strings.Join(names, ", ")
}
