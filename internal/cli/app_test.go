package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/dispatch"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// The first five exit codes mirror vb's own, so a script driving both CLIs
// branches on one set of numbers.
func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"usage", Usage("bad flag"), ExitUsage},
		{"vb not found", &vb.Error{Code: vb.ExitNotFound}, ExitNotFound},
		{"vb transition", &vb.Error{Code: vb.ExitInvalidTransition}, ExitBadTransition},
		{"vb dependency", &vb.Error{Code: vb.ExitDependency}, ExitDependency},
		{"vb lock", &vb.Error{Code: vb.ExitLockConflict}, ExitLockConflict},
		{"vb validation", &vb.Error{Code: vb.ExitValidation}, ExitValidation},
		{"run missing", fmt.Errorf("lookup: %w", runs.ErrNotFound), ExitNotFound},
		{"no workspace", fmt.Errorf("resolve: %w", workspace.ErrNotFound), ExitNotFound},
		{"locked feature", fmt.Errorf("claim: %w", dispatch.ErrLocked), ExitLockConflict},
		{"herdr incompatible", fmt.Errorf("gate: %w", herdrcli.ErrIncompatible), ExitHerdrUnavailable},
		{"herdr error", &herdrcli.Error{Code: 4, Message: "workspace_not_found"}, ExitHerdrUnavailable},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("%s: ExitCode = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Under --json the error goes to stderr in a stable envelope and stdout stays
// empty, so a caller can parse stdout unconditionally.
func TestJSONErrorEnvelope(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut, JSONFlag: true}
	app.ReportError(&vb.Error{Code: vb.ExitNotFound, Args: []string{"move"}, Message: "feature not found: FTR-9999"})

	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty under --json, got %q", out.String())
	}
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(errOut.Bytes(), &envelope); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, errOut.String())
	}
	if envelope.Error.Code != ExitNotFound {
		t.Errorf("code = %d, want %d", envelope.Error.Code, ExitNotFound)
	}
	if envelope.Error.Kind != "vb" {
		t.Errorf("kind = %q, want vb", envelope.Error.Kind)
	}
	if !strings.Contains(envelope.Error.Message, "FTR-9999") {
		t.Errorf("message lost the detail: %q", envelope.Error.Message)
	}
}

func TestUsageErrorIsKindCLI(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut, JSONFlag: true}
	app.ReportError(Usage("unknown outcome %q", "maybe"))

	if !strings.Contains(errOut.String(), `"kind": "cli"`) {
		t.Errorf("a CLI-raised failure must be kind cli: %s", errOut.String())
	}
	if !strings.Contains(errOut.String(), `"code": 64`) {
		t.Errorf("a CLI-raised failure must exit 64: %s", errOut.String())
	}
}

// A command that already rendered its detail should not print a second message.
func TestSilentErrorsPrintNothing(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	app.ReportError(errSilent{})
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing", errOut.String())
	}
	// It still fails, so a script sees a non-zero status.
	if ExitCode(errSilent{}) == ExitOK {
		t.Fatal("a silent error must still exit non-zero")
	}
}

func TestHumanErrorGoesToStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	app.ReportError(errors.New("something broke"))
	if !strings.HasPrefix(errOut.String(), "hvb: something broke") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", out.String())
	}
}

func TestEmitOnlyWritesUnderJSON(t *testing.T) {
	var out bytes.Buffer
	app := &App{Out: &out}
	if app.Emit(map[string]string{"a": "b"}) {
		t.Fatal("Emit should report false without --json")
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}

	out.Reset()
	app.JSONFlag = true
	if !app.Emit(map[string]string{"a": "b"}) {
		t.Fatal("Emit should report true under --json")
	}
	if !strings.Contains(out.String(), `"a": "b"`) {
		t.Errorf("stdout = %q", out.String())
	}
}

// The command tree is the plugin's public surface; losing a verb silently is a
// regression a user only notices at the worst moment.
func TestCommandTreeIsComplete(t *testing.T) {
	root := NewRoot(&App{})
	want := map[string][]string{
		"feature": {"list", "show", "new", "move", "set", "note", "delete", "validate"},
		"run":     {"start", "list", "show", "done", "comment", "cancel", "focus", "log"},
		"role":    {"list", "show"},
		"harness": {"list"},
	}
	for group, verbs := range want {
		command := findCommand(root, group)
		if command == nil {
			t.Errorf("missing command group %q", group)
			continue
		}
		for _, verb := range verbs {
			if findCommand(command, verb) == nil {
				t.Errorf("missing %s %s", group, verb)
			}
		}
	}
	for _, top := range []string{"tui", "doctor", "skill", "version"} {
		if findCommand(root, top) == nil {
			t.Errorf("missing top-level command %q", top)
		}
	}
}

func findCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}
