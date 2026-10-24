package playback

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Whatever Jellyfin's playlists hold, what reaches clients names only
// Overfin's neutral files, each mapped to an allowed route: no address, no
// key, no way out of the playback's own folder.
func FuzzRewrite(f *testing.F) {
	f.Add(master)
	f.Add(variant)
	f.Add("#EXTM3U\n../../../Users/x/main.m3u8\n")
	f.Fuzz(func(t *testing.T, body string) {
		for _, rw := range []func([]byte, Upstream) (*Rewritten, error){
			func(b []byte, _ Upstream) (*Rewritten, error) { return RewriteMaster(b, masterUp) },
			func(b []byte, _ Upstream) (*Rewritten, error) { return RewriteVariant(b, variantUp) },
		} {
			r, err := rw([]byte(body), Upstream{})
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(r.Body), "\n") {
				if line == "" {
					continue
				}
				if strings.Contains(line, "://") {
					t.Fatalf("an address: %q", line)
				}
				if !strings.HasPrefix(line, "#") {
					if _, ok := r.Files[line]; !ok {
						t.Fatalf("a line that isn't a file of ours: %q", line)
					}
				}
			}
			for name, up := range r.Files {
				if strings.ContainsAny(name, "/?#\\") || !allowedRoute.MatchString(up.Path) || strings.Contains(up.Path, "..") {
					t.Fatalf("file %q -> %+v", name, up)
				}
			}
		}
	})
}

// Subtitles come out as WebVTT holding cue timings and plain text only.
func FuzzSanitizeVTT(f *testing.F) {
	f.Add("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<b>Hi</b>\n")
	f.Add("WEBVTT\n\nSTYLE\n::cue { color: red }\n\n1\n00:01.000 --> 00:02.000 align:start\n<script>x</script>&lt;i&gt;\n")
	f.Fuzz(func(t *testing.T, in string) {
		out, ok := SanitizeVTT([]byte(in))
		if !ok {
			return
		}
		s := string(out)
		if !strings.HasPrefix(s, "WEBVTT\n") || !utf8.ValidString(s) {
			t.Fatalf("not WebVTT: %q", s)
		}
		for _, line := range strings.Split(s, "\n") {
			if strings.ContainsAny(line, "<\r\x00") && !cueTiming.MatchString(line) {
				t.Fatalf("markup or control characters: %q", line)
			}
			if strings.Contains(strings.NewReplacer("&amp;", "", "&lt;", "", "&gt;", "").Replace(line), "&") {
				t.Fatalf("an & that isn't escaped: %q", line)
			}
			if strings.Contains(line, "-->") && !cueTiming.MatchString(line) {
				t.Fatalf("a cue's text that reads as a timing: %q", line)
			}
		}
	})
}
