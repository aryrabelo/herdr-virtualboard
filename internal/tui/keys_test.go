package tui

import (
	"strings"
	"testing"
)

func collect(input string) []Key {
	var out []Key
	for key := range ReadKeys(strings.NewReader(input)) {
		out = append(out, key)
	}
	return out
}

func TestDecodePrintableRunes(t *testing.T) {
	keys := collect("hjkl")
	if len(keys) != 4 {
		t.Fatalf("got %d keys", len(keys))
	}
	for index, want := range []rune{'h', 'j', 'k', 'l'} {
		if keys[index].Rune != want {
			t.Errorf("key %d = %q, want %q", index, keys[index].Rune, want)
		}
	}
}

func TestDecodeArrowKeys(t *testing.T) {
	keys := collect("\x1b[A\x1b[B\x1b[C\x1b[D")
	want := []string{KeyUp, KeyDown, KeyRight, KeyLeft}
	if len(keys) != len(want) {
		t.Fatalf("got %d keys: %+v", len(keys), keys)
	}
	for index, name := range want {
		if keys[index].Name != name {
			t.Errorf("key %d = %q, want %q", index, keys[index].Name, name)
		}
	}
}

// A bare ESC is the user pressing Escape; an ESC with a sequence behind it is
// an arrow key. Getting this wrong makes arrow keys close the view.
func TestBareEscapeIsEscape(t *testing.T) {
	keys := collect("\x1b")
	if len(keys) != 1 || keys[0].Name != KeyEsc {
		t.Fatalf("got %+v, want a single esc", keys)
	}
}

func TestDecodeControlKeys(t *testing.T) {
	cases := map[string]string{
		"\r":     KeyEnter,
		"\n":     KeyEnter,
		"\t":     KeyTab,
		"\x7f":   KeyBackspace,
		"\x03":   KeyCtrlC,
		"\x0c":   KeyCtrlL,
		"\x1b[Z": KeyShiftTab,
	}
	for input, want := range cases {
		keys := collect(input)
		if len(keys) != 1 || keys[0].Name != want {
			t.Errorf("%q decoded to %+v, want %q", input, keys, want)
		}
	}
}

func TestDecodeNavigationSequences(t *testing.T) {
	cases := map[string]string{
		"\x1b[5~": KeyPageUp,
		"\x1b[6~": KeyPageDown,
		"\x1b[H":  KeyHome,
		"\x1b[F":  KeyEnd,
		"\x1bOA":  KeyUp,
	}
	for input, want := range cases {
		keys := collect(input)
		if len(keys) != 1 || keys[0].Name != want {
			t.Errorf("%q decoded to %+v, want %q", input, keys, want)
		}
	}
}

func TestDecodeMultiByteRunes(t *testing.T) {
	keys := collect("é")
	if len(keys) != 1 || keys[0].Rune != 'é' {
		t.Fatalf("got %+v, want é", keys)
	}
}

// A paste or a fast typist delivers several keys in one read; none may be lost.
func TestDecodeMixedBurst(t *testing.T) {
	keys := collect("n\x1b[Bx\rq")
	want := []string{"n", KeyDown, "x", KeyEnter, "q"}
	if len(keys) != len(want) {
		t.Fatalf("got %d keys: %+v", len(keys), keys)
	}
	for index, expected := range want {
		got := keys[index].Name
		if keys[index].Rune != 0 {
			got = string(keys[index].Rune)
		}
		if got != expected {
			t.Errorf("key %d = %q, want %q", index, got, expected)
		}
	}
}

// An unrecognised sequence must be swallowed whole, not spill its bytes into
// the board as stray keystrokes.
func TestUnknownSequenceIsConsumed(t *testing.T) {
	keys := collect("\x1b[200~a")
	if len(keys) != 2 {
		t.Fatalf("got %+v, want the unknown sequence plus 'a'", keys)
	}
	if keys[0].Name != KeyUnknown {
		t.Errorf("key 0 = %+v, want unknown", keys[0])
	}
	if keys[1].Rune != 'a' {
		t.Errorf("key 1 = %+v, want 'a'", keys[1])
	}
}
