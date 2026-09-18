package herdrcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHerdr writes a stub host CLI that records argv and replies from a case
// statement on the subcommand. The recorded argv is the point: these tests pin
// the exact command lines verified against `bora <group> --help` on bora
// 0.48.0, so a careless edit to a flag name fails here rather than at dispatch
// time.
func fakeHerdr(t *testing.T, script string) (*Client, func() []string) {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	path := filepath.Join(dir, "bora")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argvLog + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{Bin: path}, func() []string {
		raw, err := os.ReadFile(argvLog)
		if err != nil {
			return nil
		}
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
}

// supportedStatus is the real `bora status` report from bora 0.48.0 / protocol
// 25, trimmed to the keys Status parses.
const supportedStatus = `cat <<'EOF'
client:
  version: 0.48.0
  channel: preview
  protocol: 25

server:
  status: running
  version: 0.48.0
  socket: /tmp/herdr.sock
EOF`

func TestStatusParsesTheYAMLishReport(t *testing.T) {
	client, _ := fakeHerdr(t, supportedStatus)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.ClientProtocol != 25 {
		t.Errorf("ClientProtocol = %d", status.ClientProtocol)
	}
	if status.ServerVersion != "0.48.0" || !status.ServerRunning {
		t.Errorf("server = %q running=%v", status.ServerVersion, status.ServerRunning)
	}
	// The socket file is still named herdr.sock on this fork's host; only the
	// directory moved. Do not "fix" this fixture.
	if status.Socket != "/tmp/herdr.sock" {
		t.Errorf("Socket = %q", status.Socket)
	}
}

// The floor must be compared as semver, never as a string: "0.48.0" sorts
// BELOW "0.9.0" lexicographically because '4' < '9', so a string comparison
// rejects the very host this fork targets. The first assertion proves the trap
// is real; the second proves the gate is not caught by it.
func TestGateAcceptsAHostNewerThanTheVersionFloor(t *testing.T) {
	if !("0.48.0" < DefaultMinVersion) {
		t.Fatalf("the lexicographic trap is gone: %q no longer sorts below %q", "0.48.0", DefaultMinVersion)
	}
	client, _ := fakeHerdr(t, supportedStatus)
	if err := client.Gate(context.Background()); err != nil {
		t.Fatalf("Gate on herdr 0.48.0 / protocol 25 with floor %s/%d: %v",
			DefaultMinVersion, DefaultMinProtocol, err)
	}
}

// A protocol above the floor is the ordinary case on a host that keeps
// releasing; the old equality check failed every one of them.
func TestGateAcceptsAProtocolAboveTheFloor(t *testing.T) {
	client, _ := fakeHerdr(t, strings.ReplaceAll(supportedStatus, "protocol: 25", "protocol: 26"))
	if err := client.Gate(context.Background()); err != nil {
		t.Fatalf("Gate on protocol 26 with floor %d: %v", DefaultMinProtocol, err)
	}
}

// The floor is still policy, not negotiation: anything below it must fail
// before hvb creates layout or launches an agent.
func TestGateRejectsHostsBelowTheFloor(t *testing.T) {
	cases := map[string]string{
		"older major.minor": strings.ReplaceAll(supportedStatus, "version: 0.48.0\n  socket", "version: 0.8.1\n  socket"),
		"older protocol":    strings.ReplaceAll(supportedStatus, "protocol: 25", "protocol: 24"),
		"server down":       strings.ReplaceAll(supportedStatus, "status: running", "status: stopped"),
		"unparseable":       strings.ReplaceAll(supportedStatus, "version: 0.48.0\n  socket", "version: nightly\n  socket"),
	}
	for name, script := range cases {
		client, _ := fakeHerdr(t, script)
		err := client.Gate(context.Background())
		if !errors.Is(err, ErrIncompatible) {
			t.Errorf("%s: Gate = %v, want ErrIncompatible", name, err)
		}
	}
}

// The override is the operator's escape hatch: upstream Herdr 0.9.0 speaks
// protocol 22 and must be reachable without recompiling hvb.
func TestGateHonoursTheProtocolOverride(t *testing.T) {
	upstream := strings.ReplaceAll(
		strings.ReplaceAll(supportedStatus, "protocol: 25", "protocol: 22"),
		"version: 0.48.0", "version: 0.9.0")

	client, _ := fakeHerdr(t, upstream)
	if err := client.Gate(context.Background()); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("Gate on protocol 22 without an override = %v, want ErrIncompatible", err)
	}

	t.Setenv(EnvMinProtocol, "22")
	client, _ = fakeHerdr(t, upstream)
	if err := client.Gate(context.Background()); err != nil {
		t.Fatalf("Gate on protocol 22 with %s=22: %v", EnvMinProtocol, err)
	}
}

// A malformed override must fail loudly. Silently falling back to the compiled
// floor would leave an operator believing they had lowered a gate that is
// still shut.
func TestResolvedFloorRejectsMalformedOverrides(t *testing.T) {
	t.Setenv(EnvMinProtocol, "twenty-five")
	if _, err := ResolvedFloor(); err == nil {
		t.Errorf("ResolvedFloor with a non-numeric protocol = nil, want an error")
	}
	t.Setenv(EnvMinProtocol, "")
	t.Setenv(EnvMinVersion, "soon")
	if _, err := ResolvedFloor(); err == nil {
		t.Errorf("ResolvedFloor with a non-semver version = nil, want an error")
	}
}

func TestVersionOrdersFieldWiseNotLexicographically(t *testing.T) {
	cases := []struct {
		left, right string
		less        bool
	}{
		{"0.48.0", "0.9.0", false}, // the trap: true as strings
		{"0.9.0", "0.48.0", true},
		{"0.10.0", "0.9.0", false}, // the same trap one digit earlier
		{"0.9.0", "0.10.0", true},
		{"0.48.12", "0.48.9", false}, // and again in the patch field
		{"0.48.9", "0.48.12", true},
		{"0.9.0", "0.9.0", false},
		{"0.8.1", "0.9.0", true},
		{"1.0.0", "0.48.0", false},
		{"0.48.0-rc1", "0.48.0", false}, // metadata is discarded, not ranked
		{"v0.48.0", "0.48.0", false},
		{"0.9", "0.9.1", true}, // a missing field reads as zero
	}
	for _, tc := range cases {
		left, ok := parseVersion(tc.left)
		if !ok {
			t.Fatalf("parseVersion(%q) failed", tc.left)
		}
		right, ok := parseVersion(tc.right)
		if !ok {
			t.Fatalf("parseVersion(%q) failed", tc.right)
		}
		if got := left.less(right); got != tc.less {
			t.Errorf("%q less %q = %v, want %v", tc.left, tc.right, got, tc.less)
		}
	}
	for _, raw := range []string{"", "nightly", "0.x.1", "1.2.3.4", "-1.0.0"} {
		if _, ok := parseVersion(raw); ok {
			t.Errorf("parseVersion(%q) accepted a non-version", raw)
		}
	}
}

// HERDR_BIN_PATH keeps precedence, so a plugin pane uses the exact binary that
// spawned it; the fallback is this fork's own distribution name.
func TestBinaryFallsBackToBora(t *testing.T) {
	if got := (&Client{}).bin(); got != "bora" {
		t.Errorf("bin() with no HERDR_BIN_PATH = %q, want bora", got)
	}
	host := filepath.Join(t.TempDir(), "bora")
	if err := os.WriteFile(host, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBinPath, host)
	if got := New().bin(); got != host {
		t.Errorf("bin() = %q, want the HERDR_BIN_PATH value %q", got, host)
	}
}

// HERDR_BIN_PATH is re-executed with no argument review, and hvb inherits it
// from an environment a dispatched agent also writes to. Anything that is not
// already an absolute path to an executable file must be ignored: a bare name
// or a relative path resolves against PATH or against the process cwd, which
// during a dispatch is a repository an agent has been writing files into.
func TestBinPathRejectsAnythingNotAnAbsoluteExecutable(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bora")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExecutable := filepath.Join(dir, "plain")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rejected := map[string]string{
		"a bare name":        "bora",
		"a relative path":    "./bora",
		"a parent-relative":  "../bin/bora",
		"empty":              "",
		"whitespace":         "   ",
		"a missing file":     filepath.Join(dir, "absent"),
		"a directory":        dir,
		"a non-executable":   notExecutable,
		"a shell fragment":   "bora; rm -rf /",
		"a relative tempdir": filepath.Base(executable),
	}
	for name, raw := range rejected {
		if got := TrustedBinPath(raw); got != "" {
			t.Errorf("%s: TrustedBinPath(%q) = %q, want it ignored", name, raw, got)
		}
	}
	if got := TrustedBinPath(executable); got != executable {
		t.Errorf("TrustedBinPath(%q) = %q, want it trusted", executable, got)
	}

	// Ignoring the variable must fall back to PATH, never to an empty argv[0].
	t.Setenv(EnvBinPath, "bora; rm -rf /")
	if got := New().bin(); got != Binary {
		t.Errorf("bin() after a rejected HERDR_BIN_PATH = %q, want %q", got, Binary)
	}
}

func TestPanesDecodesTheEnvelope(t *testing.T) {
	client, _ := fakeHerdr(t, `cat <<'EOF'
{"id":"cli:pane:list","result":{"type":"pane_list","panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1","label":"ftr-0001","cwd":"/project","agent":"claude","agent_status":"working","focused":true},{"pane_id":"w1:p2","tab_id":"w1:t1","workspace_id":"w1","terminal_title_stripped":"zsh","cwd":"/project","agent_status":"unknown"}]}}
EOF`)
	panes, err := client.Panes(context.Background(), "w1")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("got %d panes", len(panes))
	}
	if panes[0].Name() != "ftr-0001" {
		t.Errorf("labelled pane Name() = %q", panes[0].Name())
	}
	// An unrenamed pane has no label and must fall back to its terminal title.
	if panes[1].Name() != "zsh" {
		t.Errorf("unlabelled pane Name() = %q, want the terminal title", panes[1].Name())
	}
}

func TestSplitPaneArgv(t *testing.T) {
	client, argv := fakeHerdr(t, `echo '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p9","tab_id":"w1:t1","workspace_id":"w1"}}}'`)
	pane, err := client.SplitPane(context.Background(), "w1:p1", "right", "/project",
		map[string]string{"HVB_RUN_ID": "r1", "HVB_FEATURE_ID": "FTR-0001"}, false)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if pane.PaneID != "w1:p9" {
		t.Errorf("PaneID = %q", pane.PaneID)
	}
	got := argv()[0]
	// Env must be sorted so the argv is reproducible.
	want := "pane split w1:p1 --direction right --cwd /project --env HVB_FEATURE_ID=FTR-0001 --env HVB_RUN_ID=r1 --no-focus"
	if got != want {
		t.Errorf("argv =\n  %q\nwant\n  %q", got, want)
	}
}

func TestStartAgentArgv(t *testing.T) {
	client, argv := fakeHerdr(t, "echo '{\"id\":\"x\",\"result\":{}}'")
	if err := client.StartAgent(context.Background(), "ftr-0001-ab12", "claude", "w1:p9", 90*time.Second, nil); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	want := "agent start ftr-0001-ab12 --kind claude --pane w1:p9 --timeout 90000"
	if got := argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestCreateTabArgvAndResult(t *testing.T) {
	client, argv := fakeHerdr(t, `echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"w1:t2","workspace_id":"w1","label":"ftr-0001"},"root_pane":{"pane_id":"w1:p2","tab_id":"w1:t2","workspace_id":"w1"}}}'`)
	tab, root, err := client.CreateTab(context.Background(), "w1", "/project", "ftr-0001", nil, false)
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if tab.TabID != "w1:t2" || root.PaneID != "w1:p2" {
		t.Errorf("tab=%+v root=%+v", tab, root)
	}
	want := "tab create --workspace w1 --cwd /project --label ftr-0001 --no-focus"
	if got := argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// The group name is a positional, not a flag, and it is omitted rather than
// passed empty — `usage: bora workspace set-group <workspace_id> [name]`.
func TestSetWorkspaceGroupArgv(t *testing.T) {
	client, argv := fakeHerdr(t, `echo '{"id":"x","result":{}}'`)
	if err := client.SetWorkspaceGroup(context.Background(), "w2", "bugtoprompt"); err != nil {
		t.Fatalf("SetWorkspaceGroup: %v", err)
	}
	if want, got := "workspace set-group w2 bugtoprompt", argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}

	// `$*` joins with spaces and so cannot tell an omitted last argument
	// from an empty one; the count can, and that is the difference between
	// taking a workspace out of its group and naming a group "".
	argc := filepath.Join(t.TempDir(), "argc")
	client, argv = fakeHerdr(t, `printf '%s' "$#" > `+argc+`
echo '{"id":"x","result":{}}'`)
	if err := client.SetWorkspaceGroup(context.Background(), "w2", ""); err != nil {
		t.Fatalf("SetWorkspaceGroup out of a group: %v", err)
	}
	if want, got := "workspace set-group w2", argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
	count, err := os.ReadFile(argc)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "3" {
		t.Errorf("the host got %s arguments, want 3 — the group omitted, not passed empty", count)
	}
}

// A workspace in no group reports `"visual_group":null`, which must decode as
// the empty string rather than failing the whole list: every workspace on the
// owner's session that is not in one of his groups reports it that way.
func TestWorkspacesDecodeTheSidebarGroup(t *testing.T) {
	client, _ := fakeHerdr(t, `echo '{"id":"x","result":{"workspaces":[{"workspace_id":"wA","label":"bugtoprompt-api","visual_group":"bugtoprompt"},{"workspace_id":"wB","label":"bora","visual_group":null}]}}'`)
	workspaces, err := client.Workspaces(context.Background())
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("got %d workspaces", len(workspaces))
	}
	if workspaces[0].VisualGroup != "bugtoprompt" {
		t.Errorf("grouped workspace VisualGroup = %q", workspaces[0].VisualGroup)
	}
	if workspaces[1].VisualGroup != "" {
		t.Errorf("ungrouped workspace VisualGroup = %q, want empty", workspaces[1].VisualGroup)
	}
}

// Cleanup has to be idempotent: a run can be torn down twice, once by the user
// closing the pane and once by hvb reconciling.
func TestClosePaneTreatsAMissingPaneAsClosed(t *testing.T) {
	client, _ := fakeHerdr(t, `echo '{"id":"x","error":{"code":2,"kind":"not_found","message":"pane_not_found"}}' >&2
exit 1`)
	if err := client.ClosePane(context.Background(), "w1:p9"); err != nil {
		t.Fatalf("ClosePane on a missing pane = %v, want nil", err)
	}
}

func TestErrorEnvelopeIsSurfaced(t *testing.T) {
	client, _ := fakeHerdr(t, `echo '{"id":"x","error":{"code":4,"kind":"herdr","message":"workspace_not_found"}}'
exit 0`)
	_, err := client.Panes(context.Background(), "")
	var herdrErr *Error
	if !errors.As(err, &herdrErr) {
		t.Fatalf("error = %v, want *herdrcli.Error", err)
	}
	if herdrErr.Code != 4 || herdrErr.Message != "workspace_not_found" {
		t.Errorf("error = %+v", herdrErr)
	}
}

// Herdr reports the kernel's resolved cwd, so /tmp on macOS comes back as
// /private/tmp. Comparing the strings as given would silently fail to find a
// workspace that is right there.
func TestWorkspaceForPathResolvesSymlinks(t *testing.T) {
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	client, _ := fakeHerdr(t, `cat <<EOF
{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w3:p1","workspace_id":"w3","cwd":"`+realDir+`"}]}}
EOF`)
	id, found, err := client.WorkspaceForPath(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if !found || id != "w3" {
		t.Fatalf("WorkspaceForPath through a symlink = %q, %v; want w3, true", id, found)
	}
}

// `pane read` is the one command that prints terminal content instead of a JSON
// envelope. Decoding it as one would return empty output for every pane.
func TestReadPaneReturnsRawTerminalText(t *testing.T) {
	client, argv := fakeHerdr(t, `printf 'line one\nline two\n'`)
	out, err := client.ReadPane(context.Background(), "w1:p1", "recent-unwrapped", 40)
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Fatalf("ReadPane = %q, want the terminal content", out)
	}
	want := "pane read w1:p1 --source recent-unwrapped --lines 40"
	if got := argv()[0]; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// A pane whose content happens to be JSON must come back as content, not be
// mistaken for a response envelope.
func TestReadPaneDoesNotSwallowJSONContent(t *testing.T) {
	client, _ := fakeHerdr(t, `printf '{"some":"json the pane printed"}\n'`)
	out, err := client.ReadPane(context.Background(), "w1:p1", "visible", 0)
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if !strings.Contains(out, "json the pane printed") {
		t.Fatalf("ReadPane = %q, want the pane's own output", out)
	}
}

func TestValidKindMatchesTheHerdrList(t *testing.T) {
	for _, kind := range []string{"claude", "pi", "codex", "opencode", "agy"} {
		if !ValidKind(kind) {
			t.Errorf("%s should be a valid harness", kind)
		}
	}
	for _, kind := range []string{"", "gpt", "claude-code"} {
		if ValidKind(kind) {
			t.Errorf("%q should not be a valid harness", kind)
		}
	}
}

func TestSessionFlagIsPrepended(t *testing.T) {
	client, argv := fakeHerdr(t, `echo '{"id":"x","result":{"panes":[]}}'`)
	client.Session = "work"
	if _, err := client.Panes(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := argv()[0]; got != "--session work pane list" {
		t.Fatalf("argv = %q", got)
	}
}
