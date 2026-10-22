package images

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// TMDB's image server: the only host Overfin fetches TMDB images from
// (SECURITY.md rule 5). Browsers never contact it (rule 43).
const tmdbImageBase = "https://image.tmdb.org/t/p/"

// TMDBRef is a parsed TMDB image ID: tmdb_<kind>_<id>_<type>_<file>_<ext>,
// for a catalog title's poster or backdrop. The file is the image path TMDB
// gives, without its slash and extension.
type TMDBRef struct {
	Kind   string // movie, series; "" for a library title's artwork
	TMDBID int
	Type   string // poster, backdrop, logo, still
	Path   string // as TMDB gives it: "/abc123.jpg"
}

var tmdbRefPattern = regexp.MustCompile(`^tmdb_(movie|series)_([0-9]{1,10})_(poster|backdrop)_([A-Za-z0-9]{8,64})_(jpg|png)$`)

// ParseTMDB validates a TMDB image ID.
func ParseTMDB(id string) (TMDBRef, error) {
	m := tmdbRefPattern.FindStringSubmatch(id)
	if m == nil {
		return TMDBRef{}, ErrInvalid
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return TMDBRef{}, ErrInvalid
	}
	return TMDBRef{Kind: m[1], TMDBID: n, Type: m[3], Path: "/" + m[4] + "." + m[5]}, nil
}

// artworkPattern is a library title's artwork: tmdbart_<type>_<file>_<ext>.
// The title it belongs to is found from its path.
var artworkPattern = regexp.MustCompile(`^tmdbart_(poster|backdrop|logo|still)_([A-Za-z0-9]{8,64})_(jpg|png)$`)

// ParseArtwork validates a library artwork ID.
func ParseArtwork(id string) (TMDBRef, error) {
	m := artworkPattern.FindStringSubmatch(id)
	if m == nil {
		return TMDBRef{}, ErrInvalid
	}
	return TMDBRef{Type: m[1], Path: "/" + m[2] + "." + m[3]}, nil
}

// ArtworkID is the image ID of a library title's artwork at path; "" when
// the path isn't one.
func ArtworkID(typ, path string) string {
	m := tmdbPath.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("tmdbart_%s_%s_%s", typ, m[1], m[2])
}

var tmdbPath = regexp.MustCompile(`^/([A-Za-z0-9]{8,64})\.(jpg|png)$`)

// TMDBImageID is the image ID of a title's poster or backdrop at path; ""
// when the path isn't one.
func TMDBImageID(kind string, tmdbID int, typ, path string) string {
	m := tmdbPath.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("tmdb_%s_%d_%s_%s_%s", kind, tmdbID, typ, m[1], m[2])
}

// tmdbWidths are TMDB's own sizes nearest to each of Overfin's.
var tmdbWidths = map[string]map[string]string{
	"poster":   {"small": "w185", "medium": "w342", "large": "w780"},
	"backdrop": {"small": "w780", "medium": "w1280", "large": "w1280"},
	"logo":     {"small": "w185", "medium": "w300", "large": "w500"},
	"still":    {"small": "w185", "medium": "w300", "large": "original"},
}

var tmdbHTTP = &http.Client{
	Timeout: 15 * time.Second,
	// Never follow a redirect (rule 6).
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// TMDB returns a TMDB image, re-encoded. check runs first, every time, even
// for cached images: it says whether the profile may see the title, and
// that ref's path is the title's real one, so invented paths never reach
// TMDB or the cache.
func (p *Pipeline) TMDB(ctx context.Context, ref TMDBRef, size string, check func(ctx context.Context) error) ([]byte, string, error) {
	width, ok := tmdbWidths[ref.Type][size]
	if !ok {
		return nil, "", ErrInvalid
	}
	if err := check(ctx); err != nil {
		return nil, "", err
	}
	key := "tmdb/" + ref.Path + "/" + width
	if e, ok := p.cache.get(key); ok {
		return e.data, e.contentType, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tmdbImageBase+width+ref.Path, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := tmdbHTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("images: TMDB: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, "", ErrHidden
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, jellyfin.MaxImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) > jellyfin.MaxImageBytes {
		return nil, "", ErrUnsafe
	}
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	// Logos keep their transparency.
	contentType := "image/jpeg"
	if ref.Type == "logo" {
		contentType = "image/png"
	}
	data, err := Reencode(raw, contentType)
	<-p.slots
	if err != nil {
		return nil, "", err
	}
	p.cache.put(key, entry{data: data, contentType: contentType})
	return data, contentType, nil
}
