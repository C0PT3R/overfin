package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/cursor"
	"github.com/C0PT3R/overfin-gateway/internal/images"
	"github.com/C0PT3R/overfin-gateway/internal/jellyfin"
	"github.com/C0PT3R/overfin-gateway/internal/library"
	"github.com/C0PT3R/overfin-gateway/internal/plugin"
	"github.com/C0PT3R/overfin-gateway/internal/problem"
	"github.com/C0PT3R/overfin-gateway/internal/seerr"
	"github.com/C0PT3R/overfin-gateway/internal/store"
)

// Discovery serves the catalog's discovery: what's trending, popular or
// upcoming, from Seerr, and a title's page, each with its place in the
// library (from Overfin's index) and, when the library has it, the library
// item itself, read with the profile's own token.
//
// Kids profiles see none of it yet: discovery's titles carry no rating
// Overfin has checked, so they're all hidden from them (rule 28).
type Discovery struct {
	Library Library
	Seerr   *seerr.Client
	Store   *store.Store
	Cache   *DiscoveryCache
}

func (Discovery) Name() string { return "discovery" }

func (d Discovery) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/discover":                     d.discover,
		"GET /api/v1/catalog/{mediaKind}/{tmdbId}": d.title,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// catalogTitle is a title as the API gives it (CatalogTitle).
type catalogTitle struct {
	TMDBID       string               `json:"tmdbId"`
	MediaKind    string               `json:"mediaKind"`
	Name         string               `json:"name"`
	Year         int                  `json:"year,omitempty"`
	Overview     string               `json:"overview,omitempty"`
	Images       *library.Images      `json:"images,omitempty"`
	Availability string               `json:"availability"`
	LibraryItem  *library.ItemSummary `json:"libraryItem,omitempty"`
}

type catalogSeason struct {
	Number       int    `json:"number"`
	Name         string `json:"name,omitempty"`
	EpisodeCount int    `json:"episodeCount"`
	Availability string `json:"availability"`
}

type catalogPerson struct {
	Name      string `json:"name"`
	Character string `json:"character,omitempty"`
}

type catalogDetail struct {
	catalogTitle
	Genres  []string        `json:"genres,omitempty"`
	Cast    []catalogPerson `json:"cast,omitempty"`
	Seasons []catalogSeason `json:"seasons,omitempty"`
}

type discoverPage struct {
	Items      []catalogTitle `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// seerrPages is how many of Seerr's pages (20 titles each) one request
// reads at most, to fill a page of limit titles, filtered or not.
func seerrPages(limit int) int { return min(10, limit/20+2) }

func (d Discovery) discover(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "list", "mediaKind", "availability", "limit", "cursor") {
		return
	}
	q := r.URL.Query()
	list, kind, avail := q.Get("list"), q.Get("mediaKind"), q.Get("availability")
	if avail == "" {
		avail = "all"
	}
	limit, ok := parseLimit(q.Get("limit"))
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	if (list != "trending" && list != "popular" && list != "upcoming") || (kind != "movie" && kind != "series") ||
		(avail != "all" && avail != "library" && avail != "missing") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := d.Library.active(w, r)
	if p == nil {
		return
	}
	out := discoverPage{Items: []catalogTitle{}}
	if p.Kind == "kids" {
		writeOK(w, out)
		return
	}

	binding := cursorBinding(r, p.ID)
	page := 1
	if c := q.Get("cursor"); c != "" {
		payload, err := cursor.Decode(d.Library.CursorKey, binding, c)
		if err != nil {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
		if page, err = strconv.Atoi(string(payload)); err != nil || page < 1 || page > 500 {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
	}
	more := false
	for read := 0; read < seerrPages(limit) && len(out.Items) < limit; read++ {
		sp, err := d.Cache.page(r.Context(), d.Seerr, list, kind, page)
		if err != nil {
			d.seerrError(w, err)
			return
		}
		titles, err := d.place(r.Context(), p, sp.Titles)
		if err != nil {
			d.Library.upstreamError(w, err)
			return
		}
		for _, t := range titles {
			if avail == "all" || (avail == "missing") == (t.Availability == "missing") {
				out.Items = append(out.Items, t)
			}
		}
		page++
		more = page <= sp.TotalPages
		if !more {
			break
		}
	}
	if more {
		if c, err := cursor.Encode(d.Library.CursorKey, binding, []byte(strconv.Itoa(page))); err == nil {
			out.NextCursor = c
		}
	}
	writeOK(w, out)
}

func (d Discovery) title(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	kind := r.PathValue("mediaKind")
	tmdbID, err := strconv.Atoi(r.PathValue("tmdbId"))
	if (kind != "movie" && kind != "series") || err != nil || tmdbID <= 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := d.Library.active(w, r)
	if p == nil {
		return
	}
	if p.Kind == "kids" {
		problem.Send(w, problem.NotFound, "")
		return
	}
	det, err := d.Cache.details(r.Context(), d.Seerr, kind, tmdbID)
	if err != nil {
		d.seerrError(w, err)
		return
	}
	titles, err := d.place(r.Context(), p, []seerr.Title{det.Title})
	if err != nil {
		d.Library.upstreamError(w, err)
		return
	}
	out := catalogDetail{catalogTitle: titles[0], Genres: det.Genres}
	for _, c := range det.Cast {
		out.Cast = append(out.Cast, catalogPerson{Name: c.Name, Character: c.Character})
	}
	// Each season's place, from the episodes the library has.
	var have map[int]int
	if out.LibraryItem != nil && kind == "series" {
		if have, err = d.Store.CatalogEpisodes(r.Context(), out.LibraryItem.ID[len("item_"):]); err != nil {
			d.Library.Logger.Error("reading the catalog failed", "err", err)
			problem.Send(w, problem.Internal, "")
			return
		}
	}
	for _, s := range det.Seasons {
		a := "missing"
		switch n := have[s.Number]; {
		case n > 0 && n >= s.EpisodeCount:
			a = "available"
		case n > 0:
			a = "partiallyAvailable"
		}
		out.Seasons = append(out.Seasons, catalogSeason{Number: s.Number, Name: s.Name, EpisodeCount: s.EpisodeCount, Availability: a})
	}
	writeOK(w, out)
}

// place gives titles their place in the library, as this profile sees it:
// what the index has and Jellyfin shows the profile is in the library, with
// the item itself; the rest is missing.
func (d Discovery) place(ctx context.Context, p *store.Profile, titles []seerr.Title) ([]catalogTitle, error) {
	if len(titles) == 0 {
		return nil, nil
	}
	kind := titles[0].Kind
	ids := make([]string, len(titles))
	for i, t := range titles {
		ids[i] = strconv.Itoa(t.TMDBID)
	}
	found, err := d.Store.CatalogByTMDB(ctx, kind, ids)
	if err != nil {
		return nil, err
	}
	items := map[string]library.ItemSummary{}
	if len(found) > 0 {
		var itemIDs []string
		for _, id := range found {
			itemIDs = append(itemIDs, id)
		}
		page, err := d.Library.Jellyfin.Items(ctx, auth(p), p.JellyfinUserID,
			jellyfin.ItemQuery{IDs: itemIDs, Limit: len(itemIDs)})
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			if s, ok := library.Summary(&page.Items[i]); ok {
				items[s.ID] = s
			}
		}
	}
	out := make([]catalogTitle, len(titles))
	for i, t := range titles {
		ct := catalogTitle{TMDBID: strconv.Itoa(t.TMDBID), MediaKind: t.Kind, Name: t.Name, Year: t.Year,
			Overview: t.Overview, Images: tmdbImages(t), Availability: "missing"}
		if id, ok := found[ct.TMDBID]; ok {
			if s, ok := items["item_"+id]; ok {
				ct.LibraryItem = &s
				ct.Availability = "available"
				if t.Status == 4 {
					ct.Availability = "partiallyAvailable"
				}
			}
		}
		out[i] = ct
	}
	return out, nil
}

func tmdbImages(t seerr.Title) *library.Images {
	var im library.Images
	if id := images.TMDBImageID(t.Kind, t.TMDBID, "poster", t.Poster); id != "" {
		im.Primary = &library.ImageRef{URL: "/api/v1/images/" + id}
	}
	if id := images.TMDBImageID(t.Kind, t.TMDBID, "backdrop", t.Backdrop); id != "" {
		im.Backdrops = []library.ImageRef{{URL: "/api/v1/images/" + id}}
	}
	if im.Primary == nil && im.Backdrops == nil {
		return nil
	}
	return &im
}

// image serves a catalog title's TMDB image, for profiles that may see the
// title, and only at the path Seerr gives for it.
func (d Discovery) image(w http.ResponseWriter, r *http.Request, p *store.Profile, id, size string) {
	ref, err := images.ParseTMDB(id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	data, contentType, err := d.Library.Images.TMDB(r.Context(), ref, size, func(ctx context.Context) error {
		if p.Kind == "kids" {
			return images.ErrHidden
		}
		det, err := d.Cache.details(ctx, d.Seerr, ref.Kind, ref.TMDBID)
		if errors.Is(err, seerr.ErrNotFound) {
			return images.ErrHidden
		}
		if err != nil {
			return err
		}
		if (ref.Type == "poster" && det.Poster != ref.Path) || (ref.Type == "backdrop" && det.Backdrop != ref.Path) {
			return images.ErrInvalid
		}
		return nil
	})
	switch {
	case err == nil:
	case errors.Is(err, images.ErrInvalid), errors.Is(err, images.ErrHidden):
		problem.Send(w, problem.NotFound, "")
		return
	case errors.Is(err, images.ErrUnsafe):
		d.Library.Logger.Warn("refused an unsafe image from TMDB", "image", id)
		problem.Send(w, problem.NotFound, "")
		return
	default:
		d.seerrError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (d Discovery) seerrError(w http.ResponseWriter, err error) {
	if errors.Is(err, seerr.ErrNotFound) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	d.Library.Logger.Error("seerr call failed", "err", err)
	problem.Send(w, problem.Internal, "")
}

// ---- Cache ----------------------------------------------------------------------

// DiscoveryCache keeps what Seerr said for a while, in memory only, so the
// same list or title isn't asked for again and again: lists change by the
// hour, titles more slowly.
type DiscoveryCache struct {
	mu    sync.Mutex
	pages map[string]cached[*seerr.Page]
	dets  map[string]cached[*seerr.Details]
	now   func() time.Time
}

type cached[T any] struct {
	value T
	until time.Time
}

const (
	pageFor     = 10 * time.Minute
	detailsFor  = 6 * time.Hour
	maxCacheLen = 5000
)

// NewDiscoveryCache returns an empty cache.
func NewDiscoveryCache() *DiscoveryCache {
	return &DiscoveryCache{pages: map[string]cached[*seerr.Page]{}, dets: map[string]cached[*seerr.Details]{}, now: time.Now}
}

func (c *DiscoveryCache) page(ctx context.Context, s *seerr.Client, list, kind string, page int) (*seerr.Page, error) {
	key := list + "/" + kind + "/" + strconv.Itoa(page)
	c.mu.Lock()
	e, ok := c.pages[key]
	c.mu.Unlock()
	if ok && c.now().Before(e.until) {
		return e.value, nil
	}
	v, err := s.Discover(ctx, list, kind, page)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pages) >= maxCacheLen {
		clear(c.pages)
	}
	c.pages[key] = cached[*seerr.Page]{v, c.now().Add(pageFor)}
	return v, nil
}

func (c *DiscoveryCache) details(ctx context.Context, s *seerr.Client, kind string, tmdbID int) (*seerr.Details, error) {
	key := kind + "/" + strconv.Itoa(tmdbID)
	c.mu.Lock()
	e, ok := c.dets[key]
	c.mu.Unlock()
	if ok && c.now().Before(e.until) {
		return e.value, nil
	}
	v, err := s.Details(ctx, kind, tmdbID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dets) >= maxCacheLen {
		clear(c.dets)
	}
	c.dets[key] = cached[*seerr.Details]{v, c.now().Add(detailsFor)}
	return v, nil
}
