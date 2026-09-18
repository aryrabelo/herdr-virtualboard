// Package roles reads the VirtualBoard agent charters from
// `.virtualboard/agents/` and picks the right one for a feature.
//
// A role is what herdr-board calls a "system prompt": the charter file is
// prepended to the dispatch prompt so the agent adopts the role VirtualBoard
// expects it to announce.
package roles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/fila"
	"gopkg.in/yaml.v3"
)

// Role is one VirtualBoard agent charter.
type Role struct {
	// Key is the charter's file stem (`backend_dev`). It is what the user
	// types and what `HVB_ROLE` carries.
	Key string `json:"key"`
	// Name is the `name:` frontmatter field (`backend-dev`), which is the
	// spelling the Claude Code subagent of the same charter registers under.
	Name string `json:"name"`
	// Description is the `description:` frontmatter field.
	Description string `json:"description"`
	// Path is the absolute charter path.
	Path string `json:"path"`
}

// notRoles are the files in `agents/` that describe the system rather than a
// role an agent can adopt.
var notRoles = map[string]bool{"AGENTS": true, "RULES": true, "README": true}

// Load reads every charter in the workspace's agents directory, sorted by key.
// A workspace with no agents directory yields no roles and no error: dispatch
// then falls back to a role-less prompt rather than refusing to run.
func Load(agentsDir string) ([]Role, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents directory: %w", err)
	}
	var out []Role
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		key := strings.TrimSuffix(name, ".md")
		if notRoles[key] {
			continue
		}
		path := filepath.Join(agentsDir, name)
		role := Role{Key: key, Name: key, Path: path}
		if header, err := readFrontmatter(path); err == nil {
			if header.Name != "" {
				role.Name = header.Name
			}
			role.Description = header.Description
		}
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Charter returns the full markdown of the role's charter file.
func (r Role) Charter() (string, error) {
	raw, err := os.ReadFile(r.Path)
	if err != nil {
		return "", fmt.Errorf("read charter %s: %w", r.Path, err)
	}
	return string(raw), nil
}

// Find resolves a role by key or by frontmatter name, case-insensitively and
// tolerating the `-`/`_` spelling difference between the two.
func Find(all []Role, want string) (Role, bool) {
	normalized := normalize(want)
	for _, role := range all {
		if normalize(role.Key) == normalized || normalize(role.Name) == normalized {
			return role, true
		}
	}
	return Role{}, false
}

// labelHints maps a feature label to the role key it suggests. It is a default,
// and any per-project config or explicit `--role` overrides it.
//
// Two vocabularies live here. The first ten rows follow the "Role Selection
// Guidelines" table in `.virtualboard/agents/AGENTS.md`, the upstream
// VirtualBoard workspace convention. The `rumo:*` rows are the owner's queue,
// measured: of the 33 issues labelled `project:bugtoprompt` in
// aryrabelo/ceo-bora, 16 carry `rumo:task`, 9 `rumo:grilling`, 2 `rumo:map`,
// and not one carries any label from the upstream ten — so before these rows
// every card on that board fell through to the fallback and got the same role.
//
// `folha` (26 cards) is deliberately absent. The three `rumo:*` labels already
// cover 27 cards, so its overlap with them is unknown, and the hint scan takes
// the first matching label in GitHub's own label order: a `folha` row could
// beat `rumo:grilling` and send an implementer at a card whose whole point is
// that it must not be implemented yet.
var labelHints = []struct {
	labels []string
	role   string
}{
	{[]string{"frontend", "ui", "ux-implementation", "component", "css"}, "frontend_dev"},
	{[]string{"backend", "api", "database", "auth", "server"}, "backend_dev"},
	{[]string{"fullstack", "feature", "end-to-end"}, "fullstack_dev"},
	{[]string{"infra", "infrastructure", "devops", "ci", "cd", "cicd", "deploy", "deployment", "observability"}, "devops_engineer"},
	{[]string{"security", "compliance", "privacy", "threat-model"}, "security_compliance_engineer"},
	{[]string{"data", "analytics", "metrics", "telemetry", "reporting", "etl"}, "data_analytics_engineer"},
	{[]string{"design", "ux", "wireframe", "prototype", "design-system"}, "ux_product_designer"},
	{[]string{"architecture", "adr", "tech-debt", "standards"}, "architect"},
	{[]string{"qa", "test", "testing", "regression", "e2e"}, "qa"},
	{[]string{"planning", "coordination", "sprint", "roadmap"}, "pm"},

	// The owner's `rumo:*` vocabulary. The colon survives normalize (which
	// only lowercases, trims, and folds `-` to `_`), and both the label and
	// the candidate go through it, so these match the label as GitHub
	// spells it — no second spelling to keep in sync. `rumo:grilling` is
	// already spelled once in fila, which routes it to the Review column,
	// and reads from there.
	{[]string{"rumo:task"}, "executor"},
	{[]string{fila.LabelGrilling}, "grilling"},
	{[]string{"rumo:map"}, "cartografo"},
}

// ErrHumanOnly reports a card no implementer may be dispatched onto: the work
// is the owner's own hands, and no charter here can finish it.
//
// It says what the card is, not what to do about it. The caller decides that,
// and dispatch does route it — to the one charter written for a blocked card
// (config.HumanOnlyRole), which researches the blocker and reports what the
// owner has to do rather than trying to close it. The sentinel stays the
// answer either way: this package knows the label, not the operator's
// configuration, and a caller that has no unblocker charter must refuse.
//
// It is why Suggest answers with an error instead of a second bool. That bool
// carried two facts at once — "no charter matched", where falling back to the
// configured default role is the right answer, and "do not dispatch this at
// all", where the same fallback launches an agent at a card it cannot finish.
// No caller could tell them apart, so both callers dropped it into `_`, and the
// refusal was inert: measured live on the owner's board, pressing d on
// ceo-bora#160 (labels `project:bugtoprompt hitl`) opened the role picker with
// `cartografo` preselected and `⏎ confirm` ready to launch. An error separates
// the two facts, and `role, _ := Suggest(...)` is a discard a reader and a
// linter both see, where a dropped bool was invisible to both.
var ErrHumanOnly = errors.New("only the owner can close this card")

// ErrNoCharter reports that no charter matched. It is not a refusal: the caller
// may dispatch under its own configured default role, which is what a workspace
// with no agents directory has always done.
var ErrNoCharter = errors.New("no charter matched")

// Suggest picks the role that best fits a feature, given the roles the
// workspace actually ships.
//
// Precedence: a `hitl` label refuses outright, then an explicit `role:` label
// wins, then the first label that matches a hint, then the status default
// (review is QA's), then fallback.
//
// The error tells the two failures apart and they are not interchangeable:
// ErrHumanOnly forbids the dispatch, ErrNoCharter merely leaves the role to the
// caller's default. Callers branch on them with errors.Is; treating them alike
// is the defect this signature exists to make hard.
func Suggest(all []Role, spec *feature.Spec, fallback string) (Role, error) {
	// `fila.LabelHITL` is the one label that means "only the owner's own
	// hands close this": mint a credential, approve, click a dashboard,
	// plug in hardware. fila already routes it to Blocked ahead of an
	// assignee, and here it refuses dispatch outright — same label, same
	// invariant, so it reads from the same const.
	//
	// It beats the hints and the `role:` label both: all three are card
	// content of equal authority, and no implementer charter fits a card
	// an agent cannot finish. 6 of the 33 cards on the owner's queue carry
	// it, and each dispatch there would spend tokens to learn that.
	//
	// The refusal is decided before the charter set is even looked at,
	// because it is a property of the card and not of what the workspace
	// ships: otherwise a workspace with no charters would answer
	// ErrNoCharter for a `hitl` card, and ErrNoCharter is the answer that
	// dispatches under the default role.
	if spec != nil {
		for _, label := range spec.Labels {
			if normalize(label) == normalize(fila.LabelHITL) {
				return Role{}, fmt.Errorf("%w: %s is labelled %s — that is the owner's own hands (mint a credential, approve, click a dashboard), so no implementer charter fits it", ErrHumanOnly, spec.ID, fila.LabelHITL)
			}
		}
	}
	if len(all) == 0 {
		return Role{}, fmt.Errorf("%w: this workspace ships no agent charters", ErrNoCharter)
	}
	if spec != nil {
		for _, label := range spec.Labels {
			if rest, ok := strings.CutPrefix(label, "role:"); ok {
				if role, found := Find(all, rest); found {
					return role, nil
				}
			}
		}
		for _, label := range spec.Labels {
			for _, hint := range labelHints {
				for _, candidate := range hint.labels {
					if normalize(label) != normalize(candidate) {
						continue
					}
					if role, found := Find(all, hint.role); found {
						return role, nil
					}
				}
			}
		}
		// A feature in review is being checked, not built, whatever its
		// labels say about the work that produced it.
		if spec.Status == feature.Review {
			if role, found := Find(all, "qa"); found {
				return role, nil
			}
		}
	}
	if fallback != "" {
		if role, found := Find(all, fallback); found {
			return role, nil
		}
	}
	if role, found := Find(all, "fullstack_dev"); found {
		return role, nil
	}
	return all[0], nil
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func readFrontmatter(path string) (frontmatter, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return frontmatter{}, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return frontmatter{}, fmt.Errorf("%s: no frontmatter", path)
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return frontmatter{}, fmt.Errorf("%s: unterminated frontmatter", path)
	}
	var header frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end+1]), &header); err != nil {
		return frontmatter{}, err
	}
	return header, nil
}

func normalize(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}
