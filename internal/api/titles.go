package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// Titles, whether a library has them or not (milestone 5): a film, a
// series, a season or an episode has one ID, one shape and one page, and
// everything about it comes from TMDB's data, never Jellyfin's. A library's
// copies of a title only decide whether it can be played.

// titleID is a title's ID in the API: its key, with underscores
// (movie/tmdb/949 → title_movie_tmdb_949). No part of a key has one.
func titleID(key string) string { return library.TitlePrefix + strings.ReplaceAll(key, "/", "_") }

// titleRef checks a title's ID.
func titleRef(id string) (store.TitleRef, bool) {
	rest, ok := strings.CutPrefix(id, library.TitlePrefix)
	if !ok || len(rest) > 120 {
		return store.TitleRef{}, false
	}
	return store.ParseTitleKey(strings.ReplaceAll(rest, "_", "/"))
}

type titleCast struct {
	Name      string            `json:"name"`
	Character string            `json:"character,omitempty"`
	Image     *library.ImageRef `json:"image,omitempty"`
}

// titleDetail is a title's page: a series' with its seasons, a season's
// with its episodes.
type titleDetail struct {
	library.TitleSummary
	Overview string                 `json:"overview,omitempty"`
	Tagline  string                 `json:"tagline,omitempty"`
	Genres   []string               `json:"genres,omitempty"`
	Cast     []titleCast            `json:"cast,omitempty"`
	Seasons  []library.TitleSummary `json:"seasons,omitempty"`
	Episodes []library.TitleSummary `json:"episodes,omitempty"`
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

// availability is whether a library has a title: all of it, part of it
// (a series or a season), or none.
func availability(t *store.TitleInfo) string {
	switch t.Ref.Kind {
	case "series", "season":
		switch {
		case t.Have == 0:
			return "missing"
		case t.Have >= t.Aired:
			return "available"
		}
		return "partiallyAvailable"
	}
	if len(t.Items) > 0 {
		return "available"
	}
	return "missing"
}

func summaryOf(t *store.TitleInfo) library.TitleSummary {
	s := library.TitleSummary{ID: titleID(t.Ref.Key()), Kind: t.Ref.Kind, Name: t.Name, Year: t.Year, AirDate: t.AirDate,
		DurationMs: int64(t.Details.Runtime) * 60_000, Availability: availability(t)}
	if s.Kind == "item" {
		s.Kind = "other"
		if t.ItemKind == "movie" || t.ItemKind == "episode" {
			s.Kind = t.ItemKind
		}
	}
	s.Playable = (t.Ref.Kind == "movie" || t.Ref.Kind == "episode" || t.Ref.Kind == "item") && len(t.Items) > 0
	if t.Ref.Kind == "season" || t.Ref.Kind == "episode" {
		s.Series = &library.TitleParent{ID: titleID(t.Ref.Series().Key()), Name: t.SeriesName}
		n := t.Ref.Season
		s.SeasonNumber = &n
	}
	if t.Ref.Kind == "episode" {
		n := t.Ref.Episode
		s.EpisodeNumber = &n
	}
	if t.Ref.Kind == "series" || t.Ref.Kind == "season" {
		s.EpisodeCount = &library.EpisodeCount{InLibrary: t.Have, Aired: t.Aired}
	}
	ref := func(typ, path string, aspect *float64) *library.ImageRef {
		if id := images.ArtworkID(typ, path); id != "" {
			return &library.ImageRef{URL: "/api/v1/images/" + id, AspectRatio: aspect}
		}
		return nil
	}
	im := library.Images{Logo: ref("logo", t.Logo, nil)}
	if t.Ref.Kind == "episode" {
		// An episode's picture is its still.
		im.Primary = ref("still", t.Still, &wideAspect)
		im.Thumb = im.Primary
	} else {
		im.Primary = ref("poster", t.Poster, &posterAspect)
	}
	if r := ref("backdrop", t.Backdrop, &wideAspect); r != nil {
		im.Backdrops = []library.ImageRef{*r}
	}
	if im.Primary != nil || im.Logo != nil || im.Backdrops != nil {
		s.Images = &im
	}
	return s
}

// visibleTitles says, for each title, whether the profile may see it: one
// a library has, when it may see one of its copies (libraries, hidden
// tags, kids limits); one no library has, when its rating allows it (kids
// profiles; anyone else sees it). A file no one identified, only through
// its copies.
func (l Library) visibleTitles(ctx context.Context, p *store.Profile, titles []*store.TitleInfo) ([]bool, error) {
	out := make([]bool, len(titles))
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	var copies []string
	for _, t := range titles {
		copies = append(copies, t.Items...)
	}
	var allowed map[string]bool
	if a != nil && len(copies) > 0 {
		if allowed, _, err = l.Provisioner.Store.AllowedItems(ctx, *a, copies); err != nil {
			return nil, err
		}
	}
	rated := map[string]bool{}
	for i, t := range titles {
		switch {
		case len(t.Items) > 0:
			out[i] = a == nil
			for _, id := range t.Items {
				out[i] = out[i] || allowed[id]
			}
		case t.Ref.Kind == "item" || t.Ref.TMDB() == "":
			// Nothing to show but its copies, and it has none.
		case a == nil || a.Ceiling == nil:
			out[i] = true
		default:
			kind := "movie"
			if t.Ref.Kind != "movie" {
				kind = "series"
			}
			k := kind + "/" + t.Ref.TMDB()
			ok, seen := rated[k]
			if !seen {
				if ok, err = l.Provisioner.Store.TitleAllowed(ctx, *a.Ceiling, kind, t.Ref.TMDB()); err != nil {
					return nil, err
				}
				rated[k] = ok
			}
			out[i] = ok
		}
	}
	return out, nil
}

// titleSummaries returns titles by key, in order, each once, leaving out
// those the profile may not see, with what it did with them.
func (l Library) titleSummaries(ctx context.Context, p *store.Profile, keys []string) ([]library.TitleSummary, error) {
	st := l.Provisioner.Store
	infos, err := st.TitleInfos(ctx, keys, today())
	if err != nil {
		return nil, err
	}
	var list []*store.TitleInfo
	seen := map[string]bool{}
	for _, k := range keys {
		if t := infos[k]; t != nil && !seen[k] {
			seen[k] = true
			list = append(list, t)
		}
	}
	visible, err := l.visibleTitles(ctx, p, list)
	if err != nil {
		return nil, err
	}
	var shown []string
	for i, t := range list {
		if visible[i] {
			shown = append(shown, t.Ref.Key())
		}
	}
	states, err := st.PersonalStates(ctx, p.ID, shown)
	if err != nil {
		return nil, err
	}
	out := []library.TitleSummary{}
	for i, t := range list {
		if !visible[i] {
			continue
		}
		s := summaryOf(t)
		if st, ok := states[t.Ref.Key()]; ok {
			s.UserState = stateOf(st)
		}
		out = append(out, s)
	}
	return out, nil
}

func stateOf(s store.Personal) *library.UserState {
	out := &library.UserState{PositionMs: s.PositionMs, Played: s.Played, PlayCount: s.PlayCount, Favorite: s.Favorite}
	if !s.LastPlayedAt.IsZero() {
		out.LastPlayedAt = s.LastPlayedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// describe makes sure TMDB's data for a title is kept, fetching what's
// missing now: a title no library has, or a season no one browsed yet.
// When TMDB can't answer, what's kept is shown.
func (l Library) describe(ctx context.Context, r store.TitleRef) {
	tmdbID := r.TMDB()
	if l.Titles == nil || tmdbID == "" {
		return
	}
	st := l.Provisioner.Store
	kind := r.Kind
	if kind != "movie" {
		kind = "series"
	}
	t, err := st.TitleByTMDB(ctx, kind, tmdbID)
	if err == nil && (t == nil || len(t.Details) == 0) {
		err = l.Titles.FetchTitle(ctx, kind, tmdbID)
	}
	if err == nil && (r.Kind == "season" || r.Kind == "episode") {
		var se *store.Season
		if se, err = st.SeasonByTMDB(ctx, tmdbID, r.Season); err == nil && se == nil {
			err = l.Titles.FetchSeason(ctx, tmdbID, r.Season)
		}
	}
	if err != nil && !errors.Is(err, tmdb.ErrNotFound) && ctx.Err() == nil {
		l.Logger.Warn("describing a title from TMDB failed", "title", r.Key(), "err", err)
	}
}

// title serves a title's page: a series with its seasons, a season with
// its episodes, in a library or not.
func (l Library) title(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	ref, ok := titleRef(r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	l.describe(ctx, ref)
	st := l.Provisioner.Store
	key := ref.Key()
	infos, err := st.TitleInfos(ctx, []string{key}, today())
	if err != nil {
		l.storeError(w, err)
		return
	}
	t := infos[key]
	if t == nil || (!t.Known && len(t.Items) == 0) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	sums, err := l.titleSummaries(ctx, p, []string{key})
	if err != nil {
		l.storeError(w, err)
		return
	}
	if len(sums) == 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	out := titleDetail{TitleSummary: sums[0], Overview: t.Overview, Tagline: t.Details.Tagline, Genres: t.Details.Genres}
	if n, err := strconv.Atoi(ref.TMDB()); err == nil && (ref.Kind == "movie" || ref.Kind == "series") {
		for _, c := range t.Details.Cast {
			tc := titleCast{Name: c.Name, Character: c.Character}
			if id := images.TMDBImageID(ref.Kind, n, "profile", c.Profile); id != "" {
				tc.Image = &library.ImageRef{URL: "/api/v1/images/" + id}
			}
			out.Cast = append(out.Cast, tc)
		}
	}
	switch ref.Kind {
	case "series":
		numbers, err := st.SeriesSeasons(ctx, ref, t.Details.Seasons)
		if err != nil {
			l.storeError(w, err)
			return
		}
		// Specials last.
		var keys []string
		for _, n := range numbers {
			if n > 0 {
				keys = append(keys, store.TitleRef{Kind: "season", Source: ref.Source, ID: ref.ID, Season: n}.Key())
			}
		}
		if len(numbers) > 0 && numbers[0] == 0 {
			keys = append(keys, store.TitleRef{Kind: "season", Source: ref.Source, ID: ref.ID, Season: 0}.Key())
		}
		if out.Seasons, err = l.titleSummaries(ctx, p, keys); err != nil {
			l.storeError(w, err)
			return
		}
	case "season":
		numbers, err := st.SeasonEpisodes(ctx, ref)
		if err != nil {
			l.storeError(w, err)
			return
		}
		var keys []string
		for _, n := range numbers {
			keys = append(keys, store.TitleRef{Kind: "episode", Source: ref.Source, ID: ref.ID, Season: ref.Season, Episode: n}.Key())
		}
		if out.Episodes, err = l.titleSummaries(ctx, p, keys); err != nil {
			l.storeError(w, err)
			return
		}
	}
	writeOK(w, out)
}

// visibleTitle reads a title the profile may see, or writes "not found".
func (l Library) visibleTitle(w http.ResponseWriter, r *http.Request, p *store.Profile) (*store.TitleInfo, bool) {
	ref, ok := titleRef(r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return nil, false
	}
	ctx := r.Context()
	key := ref.Key()
	infos, err := l.Provisioner.Store.TitleInfos(ctx, []string{key}, today())
	if err != nil {
		l.storeError(w, err)
		return nil, false
	}
	t := infos[key]
	if t == nil || (!t.Known && len(t.Items) == 0) {
		problem.Send(w, problem.NotFound, "")
		return nil, false
	}
	visible, err := l.visibleTitles(ctx, p, []*store.TitleInfo{t})
	if err != nil {
		l.storeError(w, err)
		return nil, false
	}
	if !visible[0] {
		problem.Send(w, problem.NotFound, "")
		return nil, false
	}
	return t, true
}

func (l Library) markTitlePlayed(w http.ResponseWriter, r *http.Request) {
	l.markTitle(w, r, true, false)
}
func (l Library) unmarkTitlePlayed(w http.ResponseWriter, r *http.Request) {
	l.markTitle(w, r, false, false)
}
func (l Library) markTitleFavorite(w http.ResponseWriter, r *http.Request) {
	l.markTitle(w, r, true, true)
}
func (l Library) unmarkTitleFavorite(w http.ResponseWriter, r *http.Request) {
	l.markTitle(w, r, false, true)
}

// markTitle marks a title watched or a favorite, or not. A series or a
// season marked watched (or not) marks its aired episodes too, those no
// library has included.
func (l Library) markTitle(w http.ResponseWriter, r *http.Request, on, favorite bool) {
	if !onlyParams(w, r) {
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	t, ok := l.visibleTitle(w, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	st := l.Provisioner.Store
	key := t.Ref.Key()
	now := time.Now()
	var err error
	if favorite {
		err = st.SetFavorite(ctx, p.ID, key, first(t.Items), on, now)
	} else {
		titles := map[string]string{key: first(t.Items)}
		episodes, eerr := l.episodesOf(ctx, t)
		if eerr != nil {
			l.storeError(w, eerr)
			return
		}
		for _, e := range episodes {
			titles[e.Ref.Key()] = first(e.Items)
		}
		err = st.SetPlayed(ctx, p.ID, titles, on, now)
	}
	if err != nil {
		l.storeError(w, err)
		return
	}
	state, err := l.userState(ctx, p, key)
	if err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, state)
}

// episodesOf lists a series' or a season's aired episodes (and any a
// library has), as titles; nothing for other titles.
func (l Library) episodesOf(ctx context.Context, t *store.TitleInfo) ([]*store.TitleInfo, error) {
	st := l.Provisioner.Store
	var seasons []int
	switch t.Ref.Kind {
	case "series":
		var err error
		if seasons, err = st.SeriesSeasons(ctx, t.Ref, t.Details.Seasons); err != nil {
			return nil, err
		}
	case "season":
		seasons = []int{t.Ref.Season}
	default:
		return nil, nil
	}
	var keys []string
	for _, n := range seasons {
		season := store.TitleRef{Kind: "season", Source: t.Ref.Source, ID: t.Ref.ID, Season: n}
		numbers, err := st.SeasonEpisodes(ctx, season)
		if err != nil {
			return nil, err
		}
		for _, e := range numbers {
			keys = append(keys, store.TitleRef{Kind: "episode", Source: t.Ref.Source, ID: t.Ref.ID, Season: n, Episode: e}.Key())
		}
	}
	day := today()
	infos, err := st.TitleInfos(ctx, keys, day)
	if err != nil {
		return nil, err
	}
	var out []*store.TitleInfo
	for _, k := range keys {
		e := infos[k]
		if e != nil && (len(e.Items) > 0 || (e.AirDate != "" && e.AirDate <= day)) {
			out = append(out, e)
		}
	}
	return out, nil
}

// forgetTitleProgress takes a title out of Continue Watching.
func (l Library) forgetTitleProgress(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	t, ok := l.visibleTitle(w, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	key := t.Ref.Key()
	if err := l.Provisioner.Store.ForgetProgress(ctx, p.ID, key); err != nil {
		l.storeError(w, err)
		return
	}
	state, err := l.userState(ctx, p, key)
	if err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, state)
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

// copyOf picks the copy of a title to play: the first one the profile may
// see, by Jellyfin ID; "" when there's none (or it isn't a title that
// plays).
func (l Library) copyOf(ctx context.Context, p *store.Profile, id string) (string, error) {
	ref, ok := titleRef(id)
	if !ok || (ref.Kind != "movie" && ref.Kind != "episode" && ref.Kind != "item") {
		return "", nil
	}
	st := l.Provisioner.Store
	infos, err := st.TitleInfos(ctx, []string{ref.Key()}, today())
	t := infos[ref.Key()]
	if err != nil || t == nil || len(t.Items) == 0 {
		return "", err
	}
	a, err := l.access(ctx, p)
	if err != nil || a == nil {
		return first(t.Items), err
	}
	allowed, _, err := st.AllowedItems(ctx, *a, t.Items)
	if err != nil {
		return "", err
	}
	for _, item := range t.Items {
		if allowed[item] {
			return item, nil
		}
	}
	return "", nil
}
