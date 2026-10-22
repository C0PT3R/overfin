package api

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/overfin/server/internal/cursor"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// Discovery serves the catalog's discovery: what's trending, popular or
// upcoming, as titles, each with its place in the library. Lists are
// answered from Overfin's database, which keeps them from TMDB in the
// background (catalog.Lists); a title's page is /titles/{id}.
//
// Kids profiles see only the titles their ceilings allow (rule 28).
type Discovery struct {
	Library Library
	Store   *store.Store
}

func (Discovery) Name() string { return "discovery" }

func (d Discovery) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/discover": d.discover,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// detailsFor is how long a title's page is kept before TMDB is asked for
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
	Name      string            `json:"name"`
	Character string            `json:"character,omitempty"`
	Image     *library.ImageRef `json:"image,omitempty"`
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
	if !ok || !slices.Contains(tmdb.Lists, list) || (kind != "movie" && kind != "series") ||
		(avail != "all" && avail != "library" && avail != "missing") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := d.Library.active(w, r)
	if p == nil {
		return
	}
	out := titlePage{Items: []library.TitleSummary{}}
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
	keys := make([]string, 0, len(titles))
	for _, t := range titles {
		keys = append(keys, t.Kind+"/tmdb/"+t.TMDB)
	}
	all, err := d.Library.titleSummaries(r.Context(), p, keys)
	if err != nil {
		d.Library.storeError(w, err)
		return
	}
	for _, t := range all {
		// The library has it, but not for this profile: not "in the
		// library" here.
		if avail == "library" && t.Availability == "missing" {
			continue
		}
		out.Items = append(out.Items, t)
	}
	writeOK(w, out)
}
