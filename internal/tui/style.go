package tui

import (
	"os"
	"strings"
	"unicode/utf8"

	"github.com/netors/herdr-virtualboard/internal/feature"
	"github.com/netors/herdr-virtualboard/internal/runs"
)

// Palette holds the SGR sequences the board draws with. When colour is off
// every field is empty, so the same render code produces plain text and the
// width arithmetic below stays correct either way.
type Palette struct {
	Reset     string
	Bold      string
	Dim       string
	Invert    string
	Backlog   string
	Progress  string
	Blocked   string
	Review    string
	Done      string
	Running   string
	Awaiting  string
	Succeeded string
	Failed    string
	Border    string
	Accent    string
	Warn      string
}

// NewPalette chooses a palette for the environment. NO_COLOR and a
// non-terminal stdout both disable colour, per the usual convention.
func NewPalette(colour bool) Palette {
	if !colour || os.Getenv("NO_COLOR") != "" {
		return Palette{}
	}
	return Palette{
		Reset:     "\x1b[0m",
		Bold:      "\x1b[1m",
		Dim:       "\x1b[2m",
		Invert:    "\x1b[7m",
		Backlog:   "\x1b[38;5;245m",
		Progress:  "\x1b[38;5;39m",
		Blocked:   "\x1b[38;5;208m",
		Review:    "\x1b[38;5;177m",
		Done:      "\x1b[38;5;78m",
		Running:   "\x1b[38;5;39m",
		Awaiting:  "\x1b[38;5;220m",
		Succeeded: "\x1b[38;5;78m",
		Failed:    "\x1b[38;5;203m",
		Border:    "\x1b[38;5;240m",
		Accent:    "\x1b[38;5;213m",
		Warn:      "\x1b[38;5;203m",
	}
}

// Status returns the colour for a lifecycle status.
func (p Palette) Status(status feature.Status) string {
	switch status {
	case feature.Backlog:
		return p.Backlog
	case feature.InProgress:
		return p.Progress
	case feature.Blocked:
		return p.Blocked
	case feature.Review:
		return p.Review
	case feature.Done:
		return p.Done
	default:
		return ""
	}
}

// RunState returns the colour for a run state.
func (p Palette) RunState(state runs.State) string {
	switch state {
	case runs.Running, runs.Queued:
		return p.Running
	case runs.Awaiting:
		return p.Awaiting
	case runs.Succeeded:
		return p.Succeeded
	case runs.Failed, runs.Cancelled:
		return p.Failed
	default:
		return ""
	}
}

// Priority returns the colour for a priority band.
func (p Palette) Priority(priority string) string {
	switch strings.ToUpper(priority) {
	case "P0":
		return p.Failed
	case "P1":
		return p.Blocked
	default:
		return p.Dim
	}
}

// paint wraps text in a colour, or returns it unchanged when colour is off.
func paint(colour, text string) string {
	if colour == "" {
		return text
	}
	return colour + text + "\x1b[0m"
}

// displayWidth counts the columns text occupies, skipping SGR sequences and
// counting a wide rune as two cells. Everything the board lays out goes through
// it, so a card title with an emoji does not push a column border off by one.
func displayWidth(text string) int {
	width := 0
	for index := 0; index < len(text); {
		if text[index] == 0x1b {
			// Skip a CSI sequence up to its final byte.
			index++
			if index < len(text) && text[index] == '[' {
				index++
				for index < len(text) && (text[index] < 0x40 || text[index] > 0x7e) {
					index++
				}
				if index < len(text) {
					index++
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(text[index:])
		width += runeWidth(r)
		index += size
	}
	return width
}

// runeWidth is a deliberately small East Asian Width approximation: the ranges
// that actually appear in a board (CJK, emoji, box drawing) rather than a full
// Unicode table hvb would have to keep up to date.
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return 0
	case r >= 0x0300 && r <= 0x036f: // combining diacriticals
		return 0
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0xa4cf, // CJK radicals … Yi
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
		r >= 0xfe30 && r <= 0xfe6f, // CJK compatibility forms
		r >= 0xff00 && r <= 0xff60, // fullwidth forms
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f, // emoji
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	default:
		return 1
	}
}

// truncate shortens text to width display columns, appending an ellipsis when
// it had to cut. Escape sequences pass through without consuming width.
func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for index := 0; index < len(text); {
		if text[index] == 0x1b {
			start := index
			index++
			if index < len(text) && text[index] == '[' {
				index++
				for index < len(text) && (text[index] < 0x40 || text[index] > 0x7e) {
					index++
				}
				if index < len(text) {
					index++
				}
			}
			b.WriteString(text[start:index])
			continue
		}
		r, size := utf8.DecodeRuneInString(text[index:])
		w := runeWidth(r)
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
		index += size
	}
	b.WriteString("…")
	return b.String()
}

// pad right-pads text to width display columns.
func pad(text string, width int) string {
	gap := width - displayWidth(text)
	if gap <= 0 {
		return text
	}
	return text + strings.Repeat(" ", gap)
}

// fit truncates and then pads, so the result is exactly width columns.
func fit(text string, width int) string { return pad(truncate(text, width), width) }

// center pads text on both sides to width columns.
func center(text string, width int) string {
	gap := width - displayWidth(text)
	if gap <= 0 {
		return truncate(text, width)
	}
	left := gap / 2
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", gap-left)
}
