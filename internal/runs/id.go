package runs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// NewID mints a run id: the feature id, a UTC timestamp, and four random bytes.
// It sorts chronologically within a feature, survives a clock that moves
// backwards, and stays short enough to read in a pane label.
func NewID(featureID string) string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// A failed CSPRNG read must not stop a dispatch; the timestamp
		// alone is already unique enough for a single user's board.
		return fmt.Sprintf("%s-%s", strings.ToLower(featureID), time.Now().UTC().Format("20060102T150405.000"))
	}
	return fmt.Sprintf("%s-%s-%s",
		strings.ToLower(featureID),
		time.Now().UTC().Format("20060102T150405"),
		hex.EncodeToString(suffix[:]))
}

// TabLabel is the stable per-feature Herdr tab name. Every run for a feature
// reuses one tab, so a feature dispatched three times does not leave three
// tabs behind.
func TabLabel(featureID string) string { return strings.ToLower(featureID) }

// PaneLabel names the pane a run's agent occupies.
func PaneLabel(featureID, role string) string {
	if role == "" {
		return strings.ToLower(featureID)
	}
	return strings.ToLower(featureID) + " · " + role
}

// AnchorLabel names the shell pane a run is split from.
func AnchorLabel(featureID string) string { return strings.ToLower(featureID) + "-anchor" }

// AgentName is the unique Herdr agent name for a run. Herdr requires
// `[a-z][a-z0-9_-]{0,31}` and uniqueness among live agents, so this is the
// feature id lowercased with a short disambiguating suffix from the run id.
func AgentName(featureID, runID string) string {
	base := strings.ToLower(strings.ReplaceAll(featureID, "_", "-"))
	suffix := runID
	if index := strings.LastIndex(runID, "-"); index >= 0 {
		suffix = runID[index+1:]
	}
	name := base + "-" + suffix
	name = sanitizeAgentName(name)
	if len(name) > 32 {
		name = name[:32]
	}
	return strings.TrimRight(name, "-_")
}

func sanitizeAgentName(name string) string {
	var out strings.Builder
	for index, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			out.WriteRune(r)
		case index > 0 && (r >= '0' && r <= '9' || r == '-' || r == '_'):
			out.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			out.WriteRune(r + 32)
		default:
			if index > 0 {
				out.WriteRune('-')
			}
		}
	}
	result := out.String()
	if result == "" || result[0] < 'a' || result[0] > 'z' {
		result = "ftr" + result
	}
	return result
}
