package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/C0PT3R/overfin-server/internal/catalog"
	"github.com/C0PT3R/overfin-server/internal/cursor"
	"github.com/C0PT3R/overfin-server/internal/images"
	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/library"
	"github.com/C0PT3R/overfin-server/internal/plugin"
	"github.com/C0PT3R/overfin-server/internal/problem"
	"github.com/C0PT3R/overfin-server/internal/seerr"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// Discovery serves the catalog's discovery: what's trending, popular or
// upcoming, and a title's page, each with its place in the library (from
// Overfin's index) and, when the library has it, the library item itself,
// read with the profile's own token. Lists and titles are answered from
// Overfin's database, which keeps them from Seerr in the background
// (catalog.Lists); only a title's page may ask Seerr, the first time it's
// opened and once a week after.
//
// Kids profiles see only the titles their ceilings allow (rule 28).
type Discovery struct {
	Library Library
	Seerr   *seerr.Client
	Store   *store.Store
	now     func() time.Time
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

func (d Discovery) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// detailsFor is how long a title's page is kept before Seerr is asked for
// it again.
const detailsFor = 7 * 24 * time.Hour

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

// storedDetails is a title's page as the catalog keeps it.
type storedDetails struct {
	Genres  []string        `json:"genres"`
	Cast    []catalogPerson `json:"cast"`
	Seasons []struct {
		Number       int    `json:"number"`
		Name         string `json:"name"`
		EpisodeCount int    `json:"episodeCount"`
	} `json:"seasons"`
}

type discoverPage struct {
	Items      []catalogTitle `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

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
	if !ok || !slices.Contains(seerr.Lists, list) || (kind != "movie" && kind != "series") ||
		(avail != "all" && avail != "library" && avail != "missing") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := d.Library.active(w, r)
	if p == nil {
		return
	}
	out := discoverPage{Items: []catalogTitle{}}
	ceiling, err := d.Library.ceiling(r.Context(), p)
	if err != nil {
		d.Library.storeError(w, err)
		return
	}
	binding := cursorBinding(r, p.ID)
	offset := 0
	if c := q.Get("cursor"); c != "" {
		payload, err := cursor.Decode(d.Library.CursorKey, binding, c)
		if err != nil {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
		if offset, err = strconv.Atoi(string(payload)); err != nil || offset < 0 {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
	}
	// One more than asked for, to know whether there are more.
	titles, err := d.Store.ListTitles(r.Context(), list, kind, avail, ceiling, offset, limit+1)
	if err != nil {
		d.Library.Logger.Error("reading the catalog failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	if len(titles) > limit {
		titles = titles[:limit]
		if c, err := cursor.Encode(d.Library.CursorKey, binding, []byte(strconv.Itoa(offset+limit))); err == nil {
			out.NextCursor = c
		}
	}
	placed, err := d.place(r.Context(), p, titles)
	if err != nil {
		d.Library.upstreamError(w, err)
		return
	}
	for _, t := range placed {
		// The library has it, but not for this profile: not "in the
		// library" here.
		if avail == "library" && t.LibraryItem == nil {
			continue
		}
		out.Items = append(out.Items, t)
	}
	writeOK(w, out)
}

func (d Discovery) title(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	kind := r.PathValue("mediaKind")
	n, err := strconv.Atoi(r.PathValue("tmdbId"))
	if (kind != "movie" && kind != "series") || err != nil || n <= 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	tmdb := strconv.Itoa(n)
	p := d.Library.active(w, r)
	if p == nil {
		return
	}
	if may, err := d.mayOpen(r.Context(), p, kind, tmdb); err != nil || !may {
		d.Library.hidden(w, err)
		return
	}
	t, err := d.Store.TitleByTMDB(r.Context(), kind, tmdb)
	if err != nil {
		d.Library.Logger.Error("reading the catalog failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	// The page, from Seerr the first time and once a week; a kept one is
	// served when Seerr can't answer.
	if t == nil || t.Details == nil || d.clock().Sub(t.DetailsAt) > detailsFor {
		fresh, err := d.fetchDetails(r.Context(), kind, n)
		switch {
		case err == nil:
			t = fresh
		case errors.Is(err, seerr.ErrNotFound):
			problem.Send(w, problem.NotFound, "")
			return
		case t == nil || t.Details == nil:
			d.seerrError(w, err)
			return
		default:
			d.Library.Logger.Warn("seerr call failed; the kept page is served", "err", err)
		}
	}
	var det storedDetails
	if err := json.Unmarshal(t.Details, &det); err != nil {
		d.Library.Logger.Error("a title's kept page is unreadable", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	placed, err := d.place(r.Context(), p, []store.Title{*t})
	if err != nil {
		d.Library.upstreamError(w, err)
		return
	}
	out := catalogDetail{catalogTitle: placed[0], Genres: det.Genres, Cast: det.Cast}
	// Each season's place, from the episodes the library has.
	var have map[int]int
	if out.LibraryItem != nil && kind == "series" {
		if have, err = d.Store.CatalogEpisodes(r.Context(), t.ItemID); err != nil {
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

// fetchDetails asks Seerr for a title's page and keeps it.
func (d Discovery) fetchDetails(ctx context.Context, kind string, tmdbID int) (*store.Title, error) {
	det, err := d.Seerr.Details(ctx, kind, tmdbID)
	if err != nil {
		return nil, err
	}
	var sd storedDetails
	sd.Genres = det.Genres
	for _, c := range det.Cast {
		sd.Cast = append(sd.Cast, catalogPerson{Name: c.Name, Character: c.Character})
	}
	for _, s := range det.Seasons {
		sd.Seasons = append(sd.Seasons, struct {
			Number       int    `json:"number"`
			Name         string `json:"name"`
			EpisodeCount int    `json:"episodeCount"`
		}{s.Number, s.Name, s.EpisodeCount})
	}
	raw, err := json.Marshal(sd)
	if err != nil {
		return nil, err
	}
	if err := d.Store.SetTitleDetails(ctx, catalog.StoreTitle(det.Title), raw, d.clock()); err != nil {
		return nil, err
	}
	t, err := d.Store.TitleByTMDB(ctx, kind, strconv.Itoa(tmdbID))
	if err == nil && t == nil {
		err = errors.New("api: a title just kept is missing")
	}
	return t, err
}

// place gives titles their place in the library, as this profile sees it:
// what the index has and Jellyfin shows the profile is in the library, with
// the item itself; the rest is missing.
func (d Discovery) place(ctx context.Context, p *store.Profile, titles []store.Title) ([]catalogTitle, error) {
	var itemIDs []string
	for _, t := range titles {
		if t.ItemID != "" {
			itemIDs = append(itemIDs, t.ItemID)
		}
	}
	items := map[string]library.ItemSummary{}
	if len(itemIDs) > 0 {
		page, err := d.Library.Jellyfin.Items(ctx, auth(p), p.JellyfinUserID,
			jellyfin.ItemQuery{IDs: itemIDs, Limit: len(itemIDs)})
		if err != nil {
			return nil, err
		}
		var found []library.ItemSummary
		for i := range page.Items {
			if s, ok := library.Summary(&page.Items[i]); ok {
				found = append(found, s)
			}
		}
		if found, err = d.Library.allowedItems(ctx, p, found); err != nil {
			return nil, err
		}
		if err := d.Library.withStates(ctx, p, found); err != nil {
			return nil, err
		}
		for _, s := range found {
			items[s.ID] = s
		}
	}
	out := make([]catalogTitle, len(titles))
	for i, t := range titles {
		ct := catalogTitle{TMDBID: t.TMDB, MediaKind: t.Kind, Name: t.Name, Year: t.Year,
			Overview: t.Overview, Images: tmdbImages(t), Availability: "missing"}
		if s, ok := items["item_"+t.ItemID]; ok && t.ItemID != "" {
			ct.LibraryItem = &s
			ct.Availability = "available"
			if t.Status == 4 {
				ct.Availability = "partiallyAvailable"
			}
		}
		out[i] = ct
	}
	return out, nil
}

func tmdbImages(t store.Title) *library.Images {
	n, _ := strconv.Atoi(t.TMDB)
	var im library.Images
	if id := images.TMDBImageID(t.Kind, n, "poster", t.Poster); id != "" {
		im.Primary = &library.ImageRef{URL: "/api/v1/images/" + id}
	}
	if id := images.TMDBImageID(t.Kind, n, "backdrop", t.Backdrop); id != "" {
		im.Backdrops = []library.ImageRef{{URL: "/api/v1/images/" + id}}
	}
	if im.Primary == nil && im.Backdrops == nil {
		return nil
	}
	return &im
}

// image serves a catalog title's TMDB image, for profiles that may see the
// title, and only at the path the catalog keeps for it.
func (d Discovery) image(w http.ResponseWriter, r *http.Request, p *store.Profile, id, size string) {
	ref, err := images.ParseTMDB(id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	data, contentType, err := d.Library.Images.TMDB(r.Context(), ref, size, func(ctx context.Context) error {
		if may, err := d.mayOpen(ctx, p, ref.Kind, strconv.Itoa(ref.TMDBID)); err != nil {
			return err
		} else if !may {
			return images.ErrHidden
		}
		t, err := d.Store.TitleByTMDB(ctx, ref.Kind, strconv.Itoa(ref.TMDBID))
		if err != nil {
			return err
		}
		if t == nil || (ref.Type == "poster" && t.Poster != ref.Path) || (ref.Type == "backdrop" && t.Backdrop != ref.Path) {
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

// mayOpen says whether the profile may see a TMDB title.
func (d Discovery) mayOpen(ctx context.Context, p *store.Profile, kind, tmdb string) (bool, error) {
	c, err := d.Library.ceiling(ctx, p)
	if c == nil || err != nil {
		return err == nil, err
	}
	return d.Store.TitleAllowed(ctx, *c, kind, tmdb)
}

func (d Discovery) seerrError(w http.ResponseWriter, err error) {
	if errors.Is(err, seerr.ErrNotFound) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	d.Library.Logger.Error("seerr call failed", "err", err)
	problem.Send(w, problem.Internal, "")
}
