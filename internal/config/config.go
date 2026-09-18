// Package config loads hvb's settings: the default harness, the per-status
// pipeline policy, and dispatch timeouts.
//
// Two files are read, in order, and the later one wins field by field:
//
//	~/.config/herdr-virtualboard/config.toml   global defaults
//	<project root>/.hvb.toml                   per-project overrides
//
// The project file sits next to `.virtualboard/`, not inside it, because
// `vb init --update` re-applies the upstream template over that directory and
// would overwrite anything hvb left there.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/git"
)

// ProjectFile is the per-project config filename, resolved against the project
// root (the directory holding `.virtualboard/`).
const ProjectFile = ".hvb.toml"

// Config is the resolved settings for one project.
type Config struct {
	// Harness is the default Herdr agent kind for dispatches.
	Harness string `toml:"harness"`
	// Role is the fallback VirtualBoard role when a feature's labels and
	// status suggest none.
	Role string `toml:"role"`
	// HumanOnlyRole is the charter key a `hitl` card is routed to. That
	// card is the owner's own hands — mint a credential, approve, click a
	// dashboard — so no implementer can close it, but one agent can still
	// research the blocker and write down what the owner has to do. 6 of
	// the 33 cards on the owner's queue carry the label, and before this
	// they only ever produced a refusal.
	//
	// It is a key, not a prompt: what that agent does lives in the charter
	// file, which is the only thing dispatch.BuildPrompt carries. An empty
	// value turns the routing off and a `hitl` card is refused outright,
	// which is also what a charter directory without this file gets — the
	// one thing neither may do is fall back to an implementer.
	HumanOnlyRole string `toml:"human_only_role"`
	// HumanOnlyHarness is the harness that charter runs under, separate
	// from Harness because the two jobs are not the same size: a dispatch
	// implements a feature, this one reads a card, researches a blocker
	// and reports back. `omp` is a harness `herdr agent start --kind`
	// accepts (herdrcli.Kinds).
	HumanOnlyHarness string `toml:"human_only_harness"`
	// Owner is the handle written to a feature's `owner` frontmatter and
	// its vb lock when hvb claims it. Empty resolves at use time from
	// $HVB_OWNER, $USER, then the hostname.
	Owner string `toml:"owner"`
	// LockTTLMinutes is the vb lock TTL a dispatch takes. Zero disables
	// locking, which is the right setting for a single-agent board.
	LockTTLMinutes int `toml:"lock_ttl_minutes"`
	// StartTimeout bounds `herdr agent start`.
	StartTimeout Duration `toml:"start_timeout"`
	// PromptTimeout bounds the first `herdr agent prompt --wait`.
	PromptTimeout Duration `toml:"prompt_timeout"`
	// Placement is where `hvb tui` opens as a plugin pane.
	Placement string `toml:"placement"`
	// Columns is the per-status pipeline policy.
	Columns map[string]Column `toml:"columns"`
	// Workflow declares the columns a sources board draws.
	Workflow Workflow `toml:"workflow"`
	// Repos is per-repository policy, keyed by owner/name.
	Repos map[string]Repo `toml:"repos"`
	// Worktree configures isolated git checkouts for dispatched agents.
	Worktree Worktree `toml:"worktree"`
	// Forge configures pull-request creation.
	Forge Forge `toml:"forge"`
	// fileColumns are the column names a config FILE declared, as opposed
	// to the five Default() ships. A declared `[workflow] columns` line
	// replaces the board, so the built-in tables are not a mistake there
	// — while a table someone wrote by hand for a column the line does not
	// draw is one, and is the typo Validate has to name. The field has no
	// TOML key: it is provenance, not settings.
	fileColumns map[string]bool `toml:"-"`
}

// Worktree configures running an agent in an isolated git checkout rather than
// in the project directory.
type Worktree struct {
	// Enabled makes a worktree the default for dispatches that do not say
	// otherwise. It stays off by default: a worktree is a real directory on
	// disk and a real branch, and creating either without being asked would
	// be a surprise.
	Enabled bool `toml:"enabled"`
	// Branch is the branch-name template. `{id}`, `{id_lower}` and `{slug}`
	// are substituted; the default matches VirtualBoard's own convention.
	Branch string `toml:"branch"`
	// Base is the branch features are cut from. Empty means the repository's
	// default branch, resolved from the remote.
	Base string `toml:"base"`
	// Remote is the git remote to branch from and push to.
	Remote string `toml:"remote"`
}

// Forge configures pull-request creation.
//
// Every field here belongs to the operator, never to a repository: Load
// refuses a project `.hvb.toml` that sets one. Kind, BaseURL and Token
// together decide which host receives an authenticated API call, so a
// repository able to set them could redirect the operator's forge token to a
// host of its choosing — and a `.hvb.toml` travels with the repository,
// including from a pull request opened by someone with no account here.
type Forge struct {
	// Enabled opens a pull request when a worktree run succeeds.
	Enabled bool `toml:"enabled"`
	// Draft opens the pull request as a draft. On by default: an agent's
	// work should be looked at before it asks for review.
	Draft bool `toml:"draft"`
	// Kind overrides forge detection for a self-hosted instance whose
	// hostname gives nothing away: "github", "forgejo", "gitea", "gitlab".
	Kind string `toml:"kind"`
	// Token authenticates the Forgejo and Gitea clients. GitHub uses the
	// gh CLI's own credentials and needs nothing here. Empty falls back to
	// $HVB_FORGE_TOKEN.
	Token string `toml:"token"`
	// BaseURL overrides the API root derived from the remote host.
	BaseURL string `toml:"base_url"`
	// PushRemotes are the git remotes hvb may publish a finished branch to.
	// The default names `origin` alone: a push is the moment an agent's work
	// leaves this machine, and which remote it leaves through is the
	// operator's decision rather than the board's. An empty list forbids
	// pushing outright.
	PushRemotes []string `toml:"push_remotes"`
	// RequireConfirmation stops hvb short of pushing and opening the pull
	// request, leaving both to the operator. Off by default, deliberately:
	// a feature that was approved into the pipeline is expected to reach a
	// pull request without anyone clicking anything, which is the point of
	// dispatching it in the first place.
	RequireConfirmation bool `toml:"require_confirmation"`
}

// ResolveToken returns the forge token, preferring configuration then the
// environment. Keeping the environment as a fallback means a token never has
// to be written into a file that might be committed.
func (f Forge) ResolveToken() string {
	if f.Token != "" {
		return f.Token
	}
	return os.Getenv("HVB_FORGE_TOKEN")
}

// AllowsRemote reports whether the operator permits pushing to a remote.
func (f Forge) AllowsRemote(name string) bool {
	for _, allowed := range f.PushRemotes {
		if allowed == name {
			return true
		}
	}
	return false
}

// Column is the policy for one lifecycle status.
type Column struct {
	// Auto dispatches an agent as soon as a feature lands in this status.
	// Without it the status is a human gate: work stops there until someone
	// moves the card on.
	Auto bool `toml:"auto"`
	// Role overrides the suggested role for features in this status.
	Role string `toml:"role"`
	// Harness overrides the default agent kind for this status.
	Harness string `toml:"harness"`
	// Prompt is prepended to the dispatch prompt, the way a herdr-board
	// column's system prompt is.
	Prompt string `toml:"prompt"`
	// PromptFromRepository records that Prompt came out of the project's
	// own `.hvb.toml` rather than the operator's config. A repository
	// cannot set this — the field has no TOML key — and dispatch presents
	// such a prompt as repository material instead of as harness policy.
	PromptFromRepository bool `toml:"-"`
	// OnSuccess and OnFailure are the statuses a finished run moves the
	// feature to. Both must be legal VirtualBoard transitions from this
	// status; Validate rejects anything else rather than letting a dispatch
	// fail later against vb.
	OnSuccess string `toml:"on_success"`
	OnFailure string `toml:"on_failure"`
	// Timeout bounds a run in this status. Zero means no bound.
	Timeout Duration `toml:"timeout"`
	// Worktree dispatches runs from this column into an isolated checkout.
	// Unset inherits the global setting; set here it wins.
	Worktree *bool `toml:"worktree"`
	// PR opens a pull request when a run from this column succeeds. Unset
	// inherits the global setting.
	PR *bool `toml:"pr"`
	// Next are the columns a card may be moved to from this one, in the
	// order the move picker offers them. It is the declared line's own
	// transition table: on the spec board vb's table is the authority and
	// this key only narrows what a picker shows.
	Next []string `toml:"next"`
	// Gate is "human" for a column work stops in until a person moves it
	// on. It is the declared spelling of what `auto = false` implies.
	Gate string `toml:"gate"`
	// Title and Short override the headings derived from the column name,
	// for the names derivation reads wrong: "PR Verification" rather than
	// "Pr Verification", "BLD" rather than a six-letter "BUILDI".
	Title string `toml:"title"`
	Short string `toml:"short"`
	// When is the label that pins a card to this column. It is a fact the
	// sources measured — a merged pull request, a closed issue — and it
	// beats anything the column store remembers.
	When string `toml:"when"`
	// Quiet marks the column a green pull request advances to once its
	// repository's quiet_timer has passed with no check and no comment. At
	// most one column may declare it.
	Quiet bool `toml:"quiet"`
}

// UseWorktree resolves the column's worktree setting against the global one.
func (c Column) UseWorktree(global bool) bool {
	if c.Worktree != nil {
		return *c.Worktree
	}
	return global
}

// UsePR resolves the column's pull-request setting against the global one.
func (c Column) UsePR(global bool) bool {
	if c.PR != nil {
		return *c.PR
	}
	return global
}

// Duration is a TOML-friendly time.Duration parsed from strings like "45m".
type Duration time.Duration

// UnmarshalText decodes a Go duration string.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalText encodes the duration back to its string form.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Default is the built-in policy. It mirrors the VirtualBoard workflow the
// agent charters describe: backlog and blocked are human gates, in-progress
// builds, review checks, and done is terminal.
func Default() Config {
	return Config{
		Harness:          "claude",
		Role:             "fullstack_dev",
		HumanOnlyRole:    "destravador",
		HumanOnlyHarness: "omp",
		LockTTLMinutes:   60,
		StartTimeout:     Duration(90 * time.Second),
		PromptTimeout:    Duration(10 * time.Minute),
		Placement:        "overlay",
		Worktree:         Worktree{Branch: git.BranchTemplate, Remote: "origin"},
		Forge:            Forge{Draft: true, PushRemotes: []string{"origin"}},
		Columns: map[string]Column{
			string(feature.Backlog): {},
			string(feature.InProgress): {
				Auto:      false,
				OnSuccess: string(feature.Review),
				OnFailure: string(feature.Blocked),
				Prompt:    "Implement this feature end to end. When the acceptance criteria are met, report success; if you are blocked by something you cannot resolve, report failure with the reason.",
			},
			string(feature.Blocked): {},
			string(feature.Review): {
				Auto:      false,
				Role:      "qa",
				OnSuccess: string(feature.Done),
				OnFailure: string(feature.InProgress),
				Prompt:    "Review this feature against its acceptance criteria. Verify the tests actually exercise the behaviour. Report success only if every criterion is met; otherwise report failure and say exactly what is missing.",
			},
			string(feature.Done): {},
		},
	}
}

// Column returns the policy for a status, falling back to an empty policy.
func (c *Config) Column(status feature.Status) Column {
	if column, ok := c.Columns[string(status)]; ok {
		return column
	}
	return Column{}
}

// Load resolves the global and project config over the defaults. A missing file
// at either layer is normal and contributes nothing.
//
// The two layers are not equivalent, and the difference is a security boundary
// rather than a convenience: the global file is the operator's, written by hand
// on this machine, while the project file arrives with the repository. A
// project file may therefore only set what a repository is allowed to decide;
// see operatorOnlyKeys.
func Load(projectRoot string) (*Config, error) {
	resolved := Default()
	globalPath, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	for _, layer := range []struct {
		path    string
		project bool
	}{
		{path: globalPath},
		{path: filepath.Join(projectRoot, ProjectFile), project: true},
	} {
		if layer.path == "" {
			continue
		}
		if err := mergeFile(&resolved, layer.path, layer.project); err != nil {
			return nil, err
		}
	}
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	return &resolved, nil
}

// GlobalPath is $HVB_CONFIG, else $XDG_CONFIG_HOME/herdr-virtualboard/config.toml,
// else ~/.config/herdr-virtualboard/config.toml.
func GlobalPath() (string, error) {
	if path := os.Getenv("HVB_CONFIG"); path != "" {
		return path, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "herdr-virtualboard", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "herdr-virtualboard", "config.toml"), nil
}

// operatorOnlyKeys are the settings only the operator's own config may set.
//
// Each one decides what hvb executes, who it authenticates to, or whether an
// ordinary dispatch turns into a push: the harness is a program hvb starts with
// the operator's credentials, the forge keys pick the host that receives their
// token, and worktree.remote names the destination a finished branch is
// published to. A `.hvb.toml` is repository content — it arrives with a clone
// and with every pull request — and for a Herdr plugin the trust boundary is
// the install, not the call: by the time this file is read, hvb already holds
// the operator's socket. A project file naming one of these is refused rather
// than ignored, because the operator should find out that the repository tried.
var operatorOnlyKeys = []string{
	"harness",
	// Same decision as `harness`, one card kind over: it names a program
	// hvb starts with the operator's credentials. `human_only_role` is
	// absent on purpose — it names a charter, which is exactly what `role`
	// already lets a repository choose.
	"human_only_harness",
	"worktree.enabled",
	"worktree.remote",
	"forge.enabled",
	"forge.draft",
	"forge.kind",
	"forge.token",
	"forge.base_url",
	"forge.push_remotes",
	"forge.require_confirmation",
}

// operatorOnlyColumnKeys are the per-column settings a project file may not
// set. They are the same three decisions one level down: which program runs,
// whether it runs in a worktree, and whether finishing publishes a branch. A
// list that stopped at the top-level keys would leave this door open.
var operatorOnlyColumnKeys = []string{"harness", "worktree", "pr"}

// refuseOperatorKeys rejects a project file that reaches for operator policy.
func refuseOperatorKeys(path string, meta toml.MetaData, overlay *Config) error {
	for _, key := range operatorOnlyKeys {
		if meta.IsDefined(strings.Split(key, ".")...) {
			return operatorKeyError(path, key)
		}
	}
	for name := range overlay.Columns {
		for _, key := range operatorOnlyColumnKeys {
			if meta.IsDefined("columns", name, key) {
				return operatorKeyError(path, fmt.Sprintf("columns.%s.%s", name, key))
			}
		}
	}
	return nil
}

func operatorKeyError(path, key string) error {
	global, err := GlobalPath()
	if err != nil || global == "" {
		global = "the hvb global config"
	}
	return fmt.Errorf("%s: %s may only be set in the operator's config (%s), not in a repository file",
		path, key, global)
}

// mergeFile overlays one TOML file onto cfg. Only the keys the file actually
// sets are applied, so a project file that names one column does not erase the
// rest of the pipeline.
//
// project marks the repository-supplied layer: it is refused the operator-only
// keys, and a column prompt it sets is remembered as repository material.
func mergeFile(cfg *Config, path string, project bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var overlay Config
	meta, err := toml.Decode(string(raw), &overlay)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return fmt.Errorf("parse %s: unknown key %q", path, undecoded[0].String())
	}
	if project {
		if err := refuseOperatorKeys(path, meta, &overlay); err != nil {
			return err
		}
	}

	if meta.IsDefined("harness") {
		cfg.Harness = overlay.Harness
	}
	if meta.IsDefined("role") {
		cfg.Role = overlay.Role
	}
	if meta.IsDefined("human_only_role") {
		cfg.HumanOnlyRole = overlay.HumanOnlyRole
	}
	if meta.IsDefined("human_only_harness") {
		cfg.HumanOnlyHarness = overlay.HumanOnlyHarness
	}
	if meta.IsDefined("owner") {
		cfg.Owner = overlay.Owner
	}
	if meta.IsDefined("lock_ttl_minutes") {
		cfg.LockTTLMinutes = overlay.LockTTLMinutes
	}
	if meta.IsDefined("start_timeout") {
		cfg.StartTimeout = overlay.StartTimeout
	}
	if meta.IsDefined("prompt_timeout") {
		cfg.PromptTimeout = overlay.PromptTimeout
	}
	if meta.IsDefined("placement") {
		cfg.Placement = overlay.Placement
	}
	for _, field := range []struct {
		key   string
		apply func()
	}{
		{"worktree.enabled", func() { cfg.Worktree.Enabled = overlay.Worktree.Enabled }},
		{"worktree.branch", func() { cfg.Worktree.Branch = overlay.Worktree.Branch }},
		{"worktree.base", func() { cfg.Worktree.Base = overlay.Worktree.Base }},
		{"worktree.remote", func() { cfg.Worktree.Remote = overlay.Worktree.Remote }},
		{"forge.enabled", func() { cfg.Forge.Enabled = overlay.Forge.Enabled }},
		{"forge.draft", func() { cfg.Forge.Draft = overlay.Forge.Draft }},
		{"forge.kind", func() { cfg.Forge.Kind = overlay.Forge.Kind }},
		{"forge.token", func() { cfg.Forge.Token = overlay.Forge.Token }},
		{"forge.base_url", func() { cfg.Forge.BaseURL = overlay.Forge.BaseURL }},
		{"forge.push_remotes", func() { cfg.Forge.PushRemotes = overlay.Forge.PushRemotes }},
		{"forge.require_confirmation", func() { cfg.Forge.RequireConfirmation = overlay.Forge.RequireConfirmation }},
	} {
		if meta.IsDefined(strings.Split(field.key, ".")...) {
			field.apply()
		}
	}
	if meta.IsDefined("workflow", "columns") {
		cfg.Workflow.Columns = overlay.Workflow.Columns
	}
	for name, repo := range overlay.Repos {
		base := cfg.Repos[name]
		if meta.IsDefined("repos", name, "quiet_timer") {
			base.QuietTimer = repo.QuietTimer
		}
		if cfg.Repos == nil {
			cfg.Repos = map[string]Repo{}
		}
		cfg.Repos[name] = base
	}
	for name, column := range overlay.Columns {
		base := cfg.Column(feature.Status(name))
		if cfg.fileColumns == nil {
			cfg.fileColumns = map[string]bool{}
		}
		cfg.fileColumns[name] = true
		if meta.IsDefined("columns", name, "auto") {
			base.Auto = column.Auto
		}
		if meta.IsDefined("columns", name, "role") {
			base.Role = column.Role
		}
		if meta.IsDefined("columns", name, "harness") {
			base.Harness = column.Harness
		}
		if meta.IsDefined("columns", name, "prompt") {
			base.Prompt = column.Prompt
			// Provenance is derived from the layer being merged, never
			// accumulated: a prompt the operator set and a project file
			// left alone stays operator policy, and a project file that
			// overrides it makes the whole prompt repository material.
			base.PromptFromRepository = project
		}
		if meta.IsDefined("columns", name, "on_success") {
			base.OnSuccess = column.OnSuccess
		}
		if meta.IsDefined("columns", name, "on_failure") {
			base.OnFailure = column.OnFailure
		}
		if meta.IsDefined("columns", name, "timeout") {
			base.Timeout = column.Timeout
		}
		if meta.IsDefined("columns", name, "worktree") {
			base.Worktree = column.Worktree
		}
		if meta.IsDefined("columns", name, "pr") {
			base.PR = column.PR
		}
		if meta.IsDefined("columns", name, "next") {
			base.Next = column.Next
		}
		if meta.IsDefined("columns", name, "gate") {
			base.Gate = column.Gate
		}
		if meta.IsDefined("columns", name, "title") {
			base.Title = column.Title
		}
		if meta.IsDefined("columns", name, "short") {
			base.Short = column.Short
		}
		if meta.IsDefined("columns", name, "when") {
			base.When = column.When
		}
		if meta.IsDefined("columns", name, "quiet") {
			base.Quiet = column.Quiet
		}
		if cfg.Columns == nil {
			cfg.Columns = map[string]Column{}
		}
		cfg.Columns[name] = base
	}
	return nil
}

// Validate rejects a configuration that would fail at dispatch time: a forge
// kind hvb has no client for, a quiet window nobody would wait for, a routing
// destination the board cannot reach.
//
// It is board-independent on purpose. The rule that every column is a vb status
// belongs to the spec board alone, and `hvb queue` renders a declared line vb
// has never heard of — so that rule is ValidateSpecBoard, asked by the command
// that opens the spec board. Load asks only what holds for both.
func (c *Config) Validate() error {
	switch git.Kind(strings.ToLower(c.Forge.Kind)) {
	case "", git.GitHub, git.Forgejo, git.Gitea, git.GitLab, git.Unknown:
	default:
		return fmt.Errorf("config: forge.kind %q is not one of github, forgejo, gitea, gitlab", c.Forge.Kind)
	}
	if err := c.validateQuietTimers(); err != nil {
		return err
	}
	if err := c.validateColumnGates(); err != nil {
		return err
	}
	if len(c.Workflow.Columns) > 0 {
		return c.validateDeclaredBoard()
	}
	return c.validateVBBoard()
}

// validateVBBoard is the check a configuration with no declared line gets: the
// board is vb's five states, so a column has to be one of them and a route has
// to be a transition vb will accept. It is what Validate has always done.
func (c *Config) validateVBBoard() error {
	vb := feature.VB()
	for _, name := range c.columnNames() {
		status, ok := vb.Parse(name)
		if !ok {
			return fmt.Errorf("config: unknown column %q (expected one of %s, or a [workflow] columns list declaring %q)",
				name, joinColumns(vb.Columns()), name)
		}
		for _, target := range routesOf(c.Columns[name]) {
			parsed, ok := vb.Parse(target.target)
			if !ok {
				return fmt.Errorf("config: column %q %s: unknown status %q", name, target.label, target.target)
			}
			if !vb.CanTransition(status, parsed) {
				return fmt.Errorf("config: column %q %s: VirtualBoard does not allow %s → %s (from %s you may move to: %s)",
					name, target.label, status, parsed, status, joinColumns(vb.Next(status)))
			}
		}
	}
	return nil
}

// ResolveOwner returns the handle hvb claims features under.
func (c *Config) ResolveOwner() string {
	if c.Owner != "" {
		return c.Owner
	}
	if owner := os.Getenv("HVB_OWNER"); owner != "" {
		return owner
	}
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "hvb"
}
