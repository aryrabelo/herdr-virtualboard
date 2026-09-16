package tui

import (
	"strings"
	"testing"
)

// Every layout decision goes through displayWidth. If it miscounts, column
// borders drift and the frame corrupts the surrounding Herdr pane.
func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"":                       0,
		"hello":                  5,
		"\x1b[1mhello\x1b[0m":    5,
		"\x1b[38;5;213mx\x1b[0m": 1,
		"日本語":                    6,
		"▶ FTR-0001":             10,
		"héllo":                  5,
	}
	for input, want := range cases {
		if got := displayWidth(input); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello world", 20); got != "hello world" {
		t.Errorf("truncate below the limit changed the text: %q", got)
	}
	got := truncate("hello world", 8)
	if displayWidth(got) > 8 {
		t.Errorf("truncate(%q, 8) = %q, width %d", "hello world", got, displayWidth(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated string should end in an ellipsis: %q", got)
	}
	if got := truncate("anything", 0); got != "" {
		t.Errorf("truncate to 0 = %q", got)
	}
}

// Colour must never cost display width, or a coloured card would wrap where an
// uncoloured one does not.
func TestTruncateKeepsEscapeSequences(t *testing.T) {
	coloured := "\x1b[38;5;213mhello world\x1b[0m"
	got := truncate(coloured, 8)
	if displayWidth(got) > 8 {
		t.Errorf("width = %d, want at most 8", displayWidth(got))
	}
	if !strings.Contains(got, "\x1b[38;5;213m") {
		t.Errorf("the opening escape was dropped: %q", got)
	}
}

func TestFitIsExact(t *testing.T) {
	for _, input := range []string{"", "short", "a much longer string than the width", "日本語テキスト"} {
		for _, width := range []int{1, 5, 12, 40} {
			if got := displayWidth(fit(input, width)); got != width {
				t.Errorf("fit(%q, %d) has width %d", input, width, got)
			}
		}
	}
}

func TestCenter(t *testing.T) {
	got := center("ab", 6)
	if displayWidth(got) != 6 {
		t.Errorf("center width = %d", displayWidth(got))
	}
	if got != "  ab  " {
		t.Errorf("center = %q", got)
	}
}

// splitWidth must use the whole terminal: a lost column of width is a visible
// ragged edge.
func TestSplitWidthUsesEverything(t *testing.T) {
	for _, total := range []int{80, 100, 120, 137, 201} {
		widths := splitWidth(total, 5)
		sum := 0
		for _, width := range widths {
			sum += width
		}
		if sum != total {
			t.Errorf("splitWidth(%d, 5) sums to %d", total, sum)
		}
	}
}

func TestScrollWindowKeepsTheSelectionVisible(t *testing.T) {
	cases := []struct{ selected, total, visible, want int }{
		{0, 10, 5, 0},
		{9, 10, 5, 5},
		{5, 10, 5, 3},
		{0, 3, 5, 0},  // everything fits
		{2, 10, 0, 0}, // no room
	}
	for _, tc := range cases {
		got := scrollWindow(tc.selected, tc.total, tc.visible)
		if got != tc.want {
			t.Errorf("scrollWindow(%d, %d, %d) = %d, want %d",
				tc.selected, tc.total, tc.visible, got, tc.want)
		}
		if tc.visible > 0 && tc.total > tc.visible {
			if tc.selected < got || tc.selected >= got+tc.visible {
				t.Errorf("scrollWindow(%d, %d, %d) = %d leaves the selection off-screen",
					tc.selected, tc.total, tc.visible, got)
			}
		}
	}
}

func TestPaletteIsEmptyWithoutColour(t *testing.T) {
	palette := NewPalette(false)
	if palette.Bold != "" || palette.Reset != "" {
		t.Fatal("a colourless palette must be entirely empty so widths stay exact")
	}
	if paint(palette.Bold, "text") != "text" {
		t.Fatal("paint with an empty colour must pass the text through")
	}
}

func TestNoColorEnvironmentDisablesColour(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if NewPalette(true).Bold != "" {
		t.Fatal("NO_COLOR must disable the palette")
	}
}

func TestWrapText(t *testing.T) {
	lines := wrapText("the quick brown fox jumps over the lazy dog", 12)
	for _, line := range lines {
		if displayWidth(line) > 12 {
			t.Errorf("line %q is %d wide", line, displayWidth(line))
		}
	}
	if strings.Join(lines, " ") != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("wrapping lost or reordered words: %v", lines)
	}
}

func TestStripUntrusted(t *testing.T) {
	got := stripUntrusted("<untrusted-content>\n<!-- note -->\n\n## Summary\nReal text.\n\n</untrusted-content>")
	if strings.Contains(got, "untrusted") || strings.Contains(got, "<!--") {
		t.Errorf("markers survived: %q", got)
	}
	if !strings.Contains(got, "Real text.") {
		t.Errorf("content was lost: %q", got)
	}
}
