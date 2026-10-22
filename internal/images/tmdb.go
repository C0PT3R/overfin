package images

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/upstream"
)

// TMDB's image server: the only host Overfin fetches TMDB images from
// (SECURITY.md rule 5). Browsers never contact it (rule 43). A variable
// for tests only.
var tmdbImageBase = "https://image.tmdb.org/t/p/"

// TMDBRef is a parsed TMDB image ID (ParseArtwork). The file is the image
// path TMDB gives, without its slash and extension.
type TMDBRef struct {
	Kind   string // movie, series; "" for a library title's artwork
	TMDBID int
	Type   string // poster, backdrop, profile (a cast member's photo), logo, still
	Path   string // as TMDB gives it: "/abc123.jpg"
}

// artworkPattern is a title's image: tmdbart_<type>_<file>_<ext>, a
// poster, backdrop, logo, still, or a cast member's photo (profile). The
// titles it belongs to are found from its path.
var artworkPattern = regexp.MustCompile(`^tmdbart_(poster|backdrop|logo|still|profile)_([A-Za-z0-9]{8,64})_(jpg|png)$`)

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

// tmdbWidths are TMDB's own sizes nearest to each of Overfin's.
var tmdbWidths = map[string]map[string]string{
	"poster":   {"small": "w185", "medium": "w342", "large": "w780"},
	"backdrop": {"small": "w780", "medium": "w1280", "large": "w1280"},
	"logo":     {"small": "w185", "medium": "w300", "large": "w500"},
	"still":    {"small": "w185", "medium": "w300", "large": "original"},
	"profile":  {"small": "w185", "medium": "h632", "large": "h632"},
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
	if data, ok := p.readKept(ref, width); ok {
		p.cache.put(key, entry{data: data, contentType: contentTypeOf(ref.Type)})
		return data, contentTypeOf(ref.Type), nil
	}
	data, err := p.fetchTMDB(ctx, ref, width, p.slots)
	if err != nil {
		return nil, "", err
	}
	p.cache.put(key, entry{data: data, contentType: contentTypeOf(ref.Type)})
	return data, contentTypeOf(ref.Type), nil
}

// contentTypeOf is what an image of type typ is encoded as: logos keep
// their transparency.
func contentTypeOf(typ string) string {
	if typ == "logo" {
		return "image/png"
	}
	return "image/jpeg"
}

// fetchTMDB fetches a TMDB image at width and re-encodes it, in one of
// slots.
func (p *Pipeline) fetchTMDB(ctx context.Context, ref TMDBRef, width string, slots chan struct{}) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tmdbImageBase+width+ref.Path, nil)
	if err != nil {
		return nil, err
	}
	res, err := tmdbHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("images: TMDB: %w", upstream.Plain(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, ErrHidden
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, jellyfin.MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > jellyfin.MaxImageBytes {
		return nil, ErrUnsafe
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, err := Reencode(raw, contentTypeOf(ref.Type))
	<-slots
	return data, err
}
