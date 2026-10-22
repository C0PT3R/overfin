package playback

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/overfin/server/internal/textclean"
)

// Subtitles reach clients only as sanitized WebVTT (SECURITY.md rule 11).
// The file is rebuilt from scratch: only cue timings and plain cue text
// survive. Cue settings, identifiers, styles, regions and notes are dropped.

const (
	maxSubtitleBytes = 5 << 20
	maxCues          = 50_000
	maxCueLine       = 500
)

var cueTiming = regexp.MustCompile(`^((?:\d{1,3}:)?\d{2}:\d{2}\.\d{3})\s+-->\s+((?:\d{1,3}:)?\d{2}:\d{2}\.\d{3})(\s.*)?$`)

// SanitizeVTT rebuilds a WebVTT file from its cue timings and plain text.
func SanitizeVTT(in []byte) ([]byte, bool) {
	if len(in) > maxSubtitleBytes {
		return nil, false
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(in), "\r\n", "\n"), "\r", "\n")
	text = strings.TrimPrefix(text, "\uFEFF") // byte order mark
	if !strings.HasPrefix(text, "WEBVTT") {
		return nil, false
	}

	var out bytes.Buffer
	out.WriteString("WEBVTT\n")
	cues := 0
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		// Find the timing line; anything before it is a cue identifier.
		timing := -1
		for i, l := range lines {
			if cueTiming.MatchString(strings.TrimSpace(l)) {
				timing = i
				break
			}
		}
		if timing < 0 {
			continue // the header, NOTE, STYLE, REGION, or junk
		}
		m := cueTiming.FindStringSubmatch(strings.TrimSpace(lines[timing]))
		var body []string
		for _, l := range lines[timing+1:] {
			if c := textclean.Plain(l, maxCueLine); c != "" {
				body = append(body, c)
			}
		}
		if len(body) == 0 {
			continue
		}
		if cues++; cues > maxCues {
			return nil, false
		}
		out.WriteString("\n" + m[1] + " --> " + m[2] + "\n" + strings.Join(body, "\n") + "\n")
	}
	return out.Bytes(), true
}
