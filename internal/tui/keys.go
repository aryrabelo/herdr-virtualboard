package tui

import (
	"bufio"
	"io"
	"unicode/utf8"
)

// Key is a decoded key press. Rune carries a printable character; the named
// constants cover everything else the board binds.
type Key struct {
	Rune rune
	Name string
}

// Named keys.
const (
	KeyUp        = "up"
	KeyDown      = "down"
	KeyLeft      = "left"
	KeyRight     = "right"
	KeyEnter     = "enter"
	KeyEsc       = "esc"
	KeyTab       = "tab"
	KeyShiftTab  = "shift-tab"
	KeyBackspace = "backspace"
	KeyPageUp    = "pgup"
	KeyPageDown  = "pgdn"
	KeyHome      = "home"
	KeyEnd       = "end"
	KeyCtrlC     = "ctrl-c"
	KeyCtrlD     = "ctrl-d"
	KeyCtrlL     = "ctrl-l"
	KeyUnknown   = "unknown"
)

// ReadKeys decodes key presses from r and sends them on the returned channel,
// which closes when r is exhausted.
//
// Escape sequences are read greedily: an ESC that is not followed by more bytes
// is a genuine Escape press, and one that is begins a CSI sequence. The
// distinction is made by buffering rather than by timing, so a slow link does
// not turn an arrow key into an Escape.
func ReadKeys(r io.Reader) <-chan Key {
	out := make(chan Key, 16)
	go func() {
		defer close(out)
		reader := bufio.NewReaderSize(r, 256)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			key, ok := decode(b, reader)
			if !ok {
				continue
			}
			out <- key
		}
	}()
	return out
}

func decode(b byte, reader *bufio.Reader) (Key, bool) {
	switch b {
	case 0x1b:
		return decodeEscape(reader)
	case '\r', '\n':
		return Key{Name: KeyEnter}, true
	case '\t':
		return Key{Name: KeyTab}, true
	case 0x7f, 0x08:
		return Key{Name: KeyBackspace}, true
	case 0x03:
		return Key{Name: KeyCtrlC}, true
	case 0x04:
		return Key{Name: KeyCtrlD}, true
	case 0x0c:
		return Key{Name: KeyCtrlL}, true
	}
	if b < 0x20 {
		return Key{Name: KeyUnknown}, true
	}
	if b < utf8.RuneSelf {
		return Key{Rune: rune(b)}, true
	}
	// Multi-byte UTF-8: collect the continuation bytes.
	buf := []byte{b}
	for len(buf) < 4 {
		next, err := reader.ReadByte()
		if err != nil {
			break
		}
		buf = append(buf, next)
		if r, size := utf8.DecodeRune(buf); r != utf8.RuneError || size > 1 {
			return Key{Rune: r}, true
		}
	}
	return Key{Name: KeyUnknown}, true
}

func decodeEscape(reader *bufio.Reader) (Key, bool) {
	if reader.Buffered() == 0 {
		return Key{Name: KeyEsc}, true
	}
	b, err := reader.ReadByte()
	if err != nil {
		return Key{Name: KeyEsc}, true
	}
	if b != '[' && b != 'O' {
		// ESC followed by something else: treat as Escape and put the byte
		// back so it is read as its own key.
		_ = reader.UnreadByte()
		return Key{Name: KeyEsc}, true
	}
	var sequence []byte
	for {
		next, err := reader.ReadByte()
		if err != nil {
			return Key{Name: KeyEsc}, true
		}
		sequence = append(sequence, next)
		if next >= 0x40 && next <= 0x7e {
			break
		}
		if len(sequence) > 16 {
			return Key{Name: KeyUnknown}, true
		}
	}
	switch string(sequence) {
	case "A":
		return Key{Name: KeyUp}, true
	case "B":
		return Key{Name: KeyDown}, true
	case "C":
		return Key{Name: KeyRight}, true
	case "D":
		return Key{Name: KeyLeft}, true
	case "H", "1~", "7~":
		return Key{Name: KeyHome}, true
	case "F", "4~", "8~":
		return Key{Name: KeyEnd}, true
	case "5~":
		return Key{Name: KeyPageUp}, true
	case "6~":
		return Key{Name: KeyPageDown}, true
	case "Z":
		return Key{Name: KeyShiftTab}, true
	default:
		return Key{Name: KeyUnknown}, true
	}
}
