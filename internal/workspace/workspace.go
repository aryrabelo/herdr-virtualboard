// Package workspace locates and describes a VirtualBoard workspace: the
// `.virtualboard/` directory `vb init` creates, and the project root that holds
// it.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// Dir is the directory name `vb init` creates in the project root.
const Dir = ".virtualboard"

// ErrNotFound is returned when no VirtualBoard workspace exists at or above the
// starting directory.
var ErrNotFound = errors.New("no VirtualBoard workspace found (run `vb init`)")

// Workspace is a resolved VirtualBoard workspace.
type Workspace struct {
	// Root is the project root: the directory containing `.virtualboard/`.
	// This is what `vb --root` expects and what agent panes get as cwd.
	Root string `json:"root"`
	// Board is `<Root>/.virtualboard`.
	Board string `json:"board"`
}

// Discover walks up from start looking for a `.virtualboard/` directory. An
// empty start means the process working directory. The search stops at the
// filesystem root; it deliberately does not stop at a git boundary, because a
// VirtualBoard workspace in a repo subdirectory is legitimate.
func Discover(start string) (*Workspace, error) {
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
		start = cwd
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", start, err)
	}
	for {
		candidate := filepath.Join(dir, Dir)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return Open(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("%w: searched from %s upwards", ErrNotFound, start)
		}
		dir = parent
	}
}

// Open resolves a workspace at an exact project root, without walking up. It
// accepts either the project root or the `.virtualboard` directory itself, so a
// user who tab-completed one directory too far still gets what they meant.
func Open(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", root, err)
	}
	if filepath.Base(abs) == Dir {
		abs = filepath.Dir(abs)
	}
	// Resolve symlinks so two paths to the same workspace produce one
	// identity; the run store keys its files on that identity.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	board := filepath.Join(abs, Dir)
	info, err := os.Stat(board)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrNotFound, board)
	}
	return &Workspace{Root: abs, Board: board}, nil
}

// Name is the project directory's base name, used for pane and tab labels.
func (w *Workspace) Name() string { return filepath.Base(w.Root) }

// ID is a stable, filesystem-safe identity for the workspace, derived from its
// absolute root. The run store uses it to keep one state file per project.
func (w *Workspace) ID() string {
	sum := sha256.Sum256([]byte(w.Root))
	return hex.EncodeToString(sum[:8])
}

// FeaturesDir is `<Board>/features`.
func (w *Workspace) FeaturesDir() string { return filepath.Join(w.Board, "features") }

// StatusDir is the directory holding specs in the given status.
func (w *Workspace) StatusDir(status feature.Status) string {
	return filepath.Join(w.FeaturesDir(), string(status))
}

// AgentsDir is `<Board>/agents`, where the VirtualBoard role charters live.
func (w *Workspace) AgentsDir() string { return filepath.Join(w.Board, "agents") }

// Rel converts an absolute path inside the workspace to a project-relative one,
// falling back to the input when it lies outside.
func (w *Workspace) Rel(path string) string {
	rel, err := filepath.Rel(w.Root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

// LoadSpecs reads every feature spec in the workspace, in board order within
// each status. A spec that fails to parse is reported rather than skipped: a
// board that silently hides a feature is worse than one that shows the error.
func (w *Workspace) LoadSpecs() ([]*feature.Spec, []error) {
	var specs []*feature.Spec
	var problems []error
	// vb owns the directories under features/, so the workspace walks vb's
	// own lifecycle rather than whatever line a board happens to draw.
	for _, status := range feature.VB().Columns() {
		dir := w.StatusDir(status)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, fmt.Errorf("read %s: %w", dir, err))
			}
			continue
		}
		var group []*feature.Spec
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			spec, err := feature.Load(filepath.Join(dir, entry.Name()))
			if err != nil {
				problems = append(problems, err)
				continue
			}
			// Trust the directory over the frontmatter: `vb move` writes
			// both, but a hand-edited spec can disagree, and the board must
			// show the feature where it actually lives.
			spec.Status = status
			group = append(group, spec)
		}
		feature.SortSpecs(group)
		specs = append(specs, group...)
	}
	return specs, problems
}

// FindSpec locates a single feature by id across every status directory.
func (w *Workspace) FindSpec(id string) (*feature.Spec, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	specs, problems := w.LoadSpecs()
	for _, spec := range specs {
		if strings.EqualFold(spec.ID, id) {
			return spec, nil
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("feature not found: %s (%d spec(s) failed to parse: %v)", id, len(problems), problems[0])
	}
	return nil, fmt.Errorf("feature not found: %s", id)
}
