package cli

import (
	"bytes"
	"strings"
	"testing"
)

// `hvb tui` and `hvb queue` read different worlds: spec markdown under
// .virtualboard, and the owner's FIOS.md, gates and pull requests. An operator
// standing in a directory that has a .virtualboard holding no specs sees five
// empty columns and has no way to learn the other board exists — that happened,
// and the empty board was read as a broken board.
//
// The board's own empty state carries the pointer too, but help is where
// someone looks before opening anything, so it has to name the sibling.
func TestTUIHelpNamesTheQueueBoard(t *testing.T) {
	app := &App{}
	cmd := newTUICommand(app)

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("hvb tui --help failed: %v", err)
	}

	help := out.String()
	if !strings.Contains(help, "hvb queue") {
		t.Errorf("tui --help never mentions the board that reads the owner's queue:\n%s", help)
	}
	if !strings.Contains(help, "FIOS.md") {
		t.Errorf("tui --help does not say what the other board reads:\n%s", help)
	}
}
