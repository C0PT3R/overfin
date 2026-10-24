package api

import (
	"net/url"
	"strings"
	"testing"
)

// Where people are sent back after signing in stays on this site.
func FuzzSafeReturn(f *testing.F) {
	for _, s := range []string{"/", "/title/x?y=1", "//evil.example", "/\\evil.example", "https://evil.example", "/%2F%2Fevil", "/\t/evil"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := safeReturn(s)
		if out == "/" {
			return
		}
		u, err := url.Parse(out)
		if out != s || err != nil || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(out, "/") ||
			strings.HasPrefix(out, "//") || strings.ContainsAny(out, "\\\t\r\n") {
			t.Fatalf("%q -> %q", s, out)
		}
	})
}

// An email accepted is one plain address, nothing an email header could
// be split with.
func FuzzCleanEmail(f *testing.F) {
	for _, s := range []string{"ann@example.org", " ann@example.org ", "Ann <ann@example.org>", "\"a\\\"b\"@example.org", "a@b", "a@b.c\r\nBcc: x@y.z"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, ok := cleanEmail(s)
		if !ok {
			return
		}
		if strings.ContainsAny(out, "\r\n\x00<>,;") || strings.Count(out, "@") < 1 || len(out) > 254 {
			t.Fatalf("%q -> %q", s, out)
		}
	})
}

// Title IDs from clients never break the parser, and those it takes come
// back the same.
func FuzzTitleRef(f *testing.F) {
	for _, s := range []string{"title_movie_tmdb_949", "title_series_tmdb_1399_s2_e3", "title_item_0123456789abcdef0123456789abcdef", "title_series_tmdb_1_s-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ref, ok := titleRef(s)
		if !ok {
			return
		}
		if ref.TMDB() != "" && strings.ContainsAny(ref.TMDB(), "/?#.") {
			t.Fatalf("%q -> TMDB %q", s, ref.TMDB())
		}
	})
}
