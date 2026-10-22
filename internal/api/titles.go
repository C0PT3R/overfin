package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
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
	// ID is the person's, for their page; absent when unknown.
	ID        string            `json:"id,omitempty"`
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
	Episodes []episodeJSON          `json:"episodes,omitempty"`
}

// episodeJSON is an episode in its season's list: with what it's about.
type episodeJSON struct {
	library.TitleSummary
	Overview string `json:"overview,omitempty"`
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
		DurationMs: int64(t.Details.Runtime) * 60_000, Availability: availability(t), Certification: t.Certification}
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
		s.Season = &library.TitleParent{ID: titleID(t.Ref.SeasonOf().Key()), Name: t.SeasonName}
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
// tags, kids limits); one no library has, when it's out as the panel asks
// (titles shown) and, for a kids profile, rated within its limits. A file
// no one identified, only through its copies. Specials a library doesn't
// have, and seasons without a single episode, never show.
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
	st := l.Provisioner.Store
	shown := "" // read when a title without copies needs it
	rated := map[string]bool{}
	type release struct {
		released, atHome string
		ok               bool
	}
	releases := map[string]release{}
	day := today()
	for i, t := range titles {
		kind := "movie"
		if t.Ref.Kind != "movie" {
			kind = "series"
		}
		k := kind + "/" + t.Ref.TMDB()
		part := t.Ref.Kind == "season" || t.Ref.Kind == "episode"
		switch {
		case len(t.Items) > 0:
			out[i] = a == nil
			for _, id := range t.Items {
				out[i] = out[i] || allowed[id]
			}
			continue
		case t.Ref.Kind == "item" || t.Ref.TMDB() == "":
			continue // nothing to show but its copies, and it has none
		case part && t.Ref.Season == 0 && (t.Ref.Kind == "episode" || t.Have == 0):
			continue // specials: only those a library has, and their season
		case t.Ref.Kind == "season" && t.Have == 0 && t.Listed == 0:
			continue // a season without a single episode
		}
		// Out yet, as the panel asks (titles shown): a season or an episode
		// once aired, its series or a film once released, or once it can
		// be watched at home. Not known yet means not out.
		if shown == "" {
			if shown, err = st.TitlesShown(ctx); err != nil {
				return nil, err
			}
		}
		if shown != "all" {
			if part && (t.AirDate == "" || t.AirDate > day) {
				continue
			}
			r, seen := releases[k]
			if !seen {
				if r.released, r.atHome, r.ok, err = st.TitleRelease(ctx, kind, t.Ref.TMDB()); err != nil {
					return nil, err
				}
				releases[k] = r
			}
			atHome := r.atHome != "" && r.atHome <= day
			if !r.ok || (shown == "home" && !atHome) || (shown == "released" && !atHome && (r.released == "" || r.released > day)) {
				continue
			}
		}
		// A kids profile: only what's rated within its limits.
		if a != nil && a.Ceiling != nil {
			ok, seen := rated[k]
			if !seen {
				if ok, err = st.TitleAllowed(ctx, *a.Ceiling, kind, t.Ref.TMDB()); err != nil {
					return nil, err
				}
				rated[k] = ok
			}
			if !ok {
				continue
			}
		}
		out[i] = true
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
	requests, err := l.requestsCovering(ctx, p.AccountID, list)
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
		s.Request = requests[t.Ref.Key()]
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
	for _, c := range t.Details.Cast {
		tc := titleCast{Name: c.Name, Character: c.Character}
		if c.TMDB != "" {
			tc.ID = personID(c.TMDB)
		}
		if id := images.ArtworkID("profile", c.Profile); id != "" {
			tc.Image = &library.ImageRef{URL: "/api/v1/images/" + id}
		}
		out.Cast = append(out.Cast, tc)
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
		episodes, err := l.titleSummaries(ctx, p, keys)
		if err != nil {
			l.storeError(w, err)
			return
		}
		infos, err := st.TitleInfos(ctx, keys, today())
		if err != nil {
			l.storeError(w, err)
			return
		}
		for _, e := range episodes {
			ej := episodeJSON{TitleSummary: e}
			if ref, ok := titleRef(e.ID); ok && infos[ref.Key()] != nil {
				ej.Overview = infos[ref.Key()].Overview
			}
			out.Episodes = append(out.Episodes, ej)
		}
	}
	writeOK(w, out)
}

// visibleTitle reads the title the request's path names, when the profile
// may see it, or writes "not found".
func (l Library) visibleTitle(w http.ResponseWriter, r *http.Request, p *store.Profile) (*store.TitleInfo, bool) {
	ref, ok := titleRef(r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return nil, false
	}
	return l.visibleTitleRef(w, r.Context(), p, ref)
}

// visibleTitleRef reads a title the profile may see, or writes "not
// found".
func (l Library) visibleTitleRef(w http.ResponseWriter, ctx context.Context, p *store.Profile, ref store.TitleRef) (*store.TitleInfo, bool) {
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

// requestsCovering finds, for titles, the account's latest request covering
// each: a film's or a series' own, and for a season or an episode, its
// series' when it asked for every season or for that one.
func (l Library) requestsCovering(ctx context.Context, accountID string, titles []*store.TitleInfo) (map[string]*library.TitleRequest, error) {
	var keys []string
	for _, t := range titles {
		switch t.Ref.Kind {
		case "movie", "series":
			keys = append(keys, t.Ref.Key())
		case "season", "episode":
			keys = append(keys, t.Ref.Series().Key())
		}
	}
	all, err := l.Provisioner.Store.AccountRequests(ctx, accountID, keys)
	if err != nil {
		return nil, err
	}
	out := map[string]*library.TitleRequest{}
	for _, t := range titles {
		for _, x := range all { // the latest first
			covers := x.Title == t.Ref.Key()
			if t.Ref.Kind == "season" || t.Ref.Kind == "episode" {
				covers = x.Title == t.Ref.Series().Key() && (len(x.Seasons) == 0 || slices.Contains(x.Seasons, t.Ref.Season))
			}
			if covers {
				out[t.Ref.Key()] = &library.TitleRequest{ID: x.ID, Status: x.Status}
				break
			}
		}
		// Someone else's request, in Seerr: it's requested, but not by whom,
		// and only its requester may cancel it.
		if out[t.Ref.Key()] != nil || l.Requested == nil {
			continue
		}
		key, season := t.Ref.Key(), -1
		if t.Ref.Kind == "season" || t.Ref.Kind == "episode" {
			key, season = t.Ref.Series().Key(), t.Ref.Season
		}
		for _, r := range l.Requested.Lookup(key) {
			if season < 0 || len(r.Seasons) == 0 || slices.Contains(r.Seasons, season) {
				out[t.Ref.Key()] = &library.TitleRequest{Status: statusOf(r)}
				break
			}
		}
	}
	return out, nil
}

// nextEpisode is the episode after ep that the profile may see: the next
// ones of its season, else the first ones of the seasons after it
// (specials only after a special); nil when there's none.
func (l Library) nextEpisode(ctx context.Context, p *store.Profile, ep store.TitleRef) (*library.TitleSummary, error) {
	st := l.Provisioner.Store
	var keys []string
	add := func(season int, after int) error {
		numbers, err := st.SeasonEpisodes(ctx, store.TitleRef{Kind: "season", Source: ep.Source, ID: ep.ID, Season: season})
		if err != nil {
			return err
		}
		for _, n := range numbers {
			if n > after && len(keys) < 10 {
				keys = append(keys, store.TitleRef{Kind: "episode", Source: ep.Source, ID: ep.ID, Season: season, Episode: n}.Key())
			}
		}
		return nil
	}
	if err := add(ep.Season, ep.Episode); err != nil {
		return nil, err
	}
	if len(keys) == 0 && ep.Season > 0 {
		series := ep.Series()
		infos, err := st.TitleInfos(ctx, []string{series.Key()}, today())
		if err != nil {
			return nil, err
		}
		var listed []store.SeasonInfo
		if t := infos[series.Key()]; t != nil {
			listed = t.Details.Seasons
		}
		seasons, err := st.SeriesSeasons(ctx, series, listed)
		if err != nil {
			return nil, err
		}
		for _, n := range seasons {
			if n > ep.Season && len(keys) == 0 {
				if err := add(n, -1); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	// The first the profile may see (one not out yet isn't shown).
	sums, err := l.titleSummaries(ctx, p, keys)
	if err != nil || len(sums) == 0 {
		return nil, err
	}
	return &sums[0], nil
}
