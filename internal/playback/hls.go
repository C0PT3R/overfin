package playback

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// HLS playlists from Jellyfin are parsed, never searched and replaced
// (SECURITY.md rule 8). Every URI in them carries Jellyfin's token; each one
// is replaced by a neutral name, and the real URL stays on the server. Tags
// outside an allow-list are dropped, including any that point to other URIs
// (alternate renditions, subtitles, keys, I-frame playlists).

// Limits on what Overfin accepts from Jellyfin.
const (
	maxPlaylistBytes = 2 << 20
	maxSegments      = 50_000
)

// ErrBadPlaylist: a playlist Overfin can't vouch for.
var ErrBadPlaylist = errors.New("playback: unexpected playlist content")

var (
	// Tags allowed through, by playlist kind.
	masterTags  = []string{"EXTM3U", "EXT-X-VERSION", "EXT-X-INDEPENDENT-SEGMENTS", "EXT-X-STREAM-INF"}
	variantTags = []string{"EXTM3U", "EXT-X-VERSION", "EXT-X-TARGETDURATION", "EXT-X-MEDIA-SEQUENCE",
		"EXT-X-PLAYLIST-TYPE", "EXT-X-INDEPENDENT-SEGMENTS", "EXTINF", "EXT-X-DISCONTINUITY",
		"EXT-X-ENDLIST", "EXT-X-MAP"}

	tagLine = regexp.MustCompile(`^#(EXT[A-Z0-9-]*)(:[\x20-\x7e]*)?$`)
	// URIs as Jellyfin writes them, relative to the playlist.
	variantURI = regexp.MustCompile(`^([A-Za-z0-9_-]{1,32})\.m3u8$`)
	segmentURI = regexp.MustCompile(`^hls1/([A-Za-z0-9_-]{1,32})/(-?[0-9]{1,6})\.(mp4|m4s|ts|aac|mp3)$`)
	mapURI     = regexp.MustCompile(`^#EXT-X-MAP:URI="([^"]*)"$`)
	// Where those URIs may lead on Jellyfin.
	allowedRoute = regexp.MustCompile(`(?i)^/(videos|audio)/[0-9a-f-]{32,36}/([A-Za-z0-9_-]{1,32}\.m3u8|hls1/[A-Za-z0-9_-]{1,32}/-?[0-9]{1,6}\.(mp4|m4s|ts|aac|mp3))$`)
)

// Rewritten is a playlist safe to send, and the real URL behind each
// neutral name in it.
type Rewritten struct {
	Body  []byte
	Files map[string]Upstream
}

// RewriteMaster rewrites a master playlist fetched from upstream. Variant
// playlists become v0.m3u8, v1.m3u8...
func RewriteMaster(body []byte, upstream Upstream) (*Rewritten, error) {
	return rewrite(body, upstream, masterTags, func(uri string, n int) (string, *regexp.Regexp) {
		return fmt.Sprintf("v%d.m3u8", n), variantURI
	})
}

// RewriteVariant rewrites a variant playlist. Segments become seg0.mp4,
// seg1.mp4... and the initialization segment init.mp4.
func RewriteVariant(body []byte, upstream Upstream) (*Rewritten, error) {
	return rewrite(body, upstream, variantTags, func(uri string, n int) (string, *regexp.Regexp) {
		m := segmentURI.FindStringSubmatch(stripQuery(uri))
		if m == nil {
			return "", segmentURI
		}
		if m[2] == "-1" {
			return "init." + m[3], segmentURI
		}
		return "seg" + m[2] + "." + m[3], segmentURI
	})
}

func stripQuery(uri string) string {
	p, _, _ := strings.Cut(uri, "?")
	return p
}

// rewrite does the work for both kinds. name gives the neutral name for the
// nth URI and the pattern the URI's path must match.
func rewrite(body []byte, upstream Upstream, tags []string,
	name func(uri string, n int) (string, *regexp.Regexp)) (*Rewritten, error) {

	if len(body) > maxPlaylistBytes {
		return nil, ErrBadPlaylist
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "#EXTM3U" {
		return nil, ErrBadPlaylist
	}

	out := Rewritten{Files: map[string]Upstream{}}
	var b bytes.Buffer
	count := 0
	// resolve turns a URI from the playlist into a checked upstream URL and
	// records it under its neutral name.
	resolve := func(uri string) (string, error) {
		if count >= maxSegments {
			return "", ErrBadPlaylist
		}
		neutral, pattern := name(uri, count)
		u, err := url.Parse(uri)
		if err != nil || neutral == "" || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" ||
			!pattern.MatchString(u.Path) {
			return "", ErrBadPlaylist
		}
		target := path.Join(path.Dir(upstream.Path), u.Path)
		if !allowedRoute.MatchString(target) || strings.Contains(u.Path, "..") {
			return "", ErrBadPlaylist
		}
		out.Files[neutral] = Upstream{Path: target, Query: u.Query()}
		count++
		return neutral, nil
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			m := tagLine.FindStringSubmatch(line)
			if m == nil || !contains(tags, m[1]) {
				continue // comments and unknown tags are dropped
			}
			if m[1] == "EXT-X-MAP" {
				mm := mapURI.FindStringSubmatch(line)
				if mm == nil {
					return nil, ErrBadPlaylist
				}
				neutral, err := resolve(mm[1])
				if err != nil {
					return nil, err
				}
				line = `#EXT-X-MAP:URI="` + neutral + `"`
			} else if strings.Contains(line, "URI=") {
				continue // any other tag pointing elsewhere is dropped
			}
			b.WriteString(line + "\n")
		default:
			neutral, err := resolve(line)
			if err != nil {
				return nil, err
			}
			b.WriteString(neutral + "\n")
		}
	}
	out.Body = b.Bytes()
	return &out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
