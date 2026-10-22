package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// TitleSource is where the library's titles are described: TMDB itself.
type TitleSource interface {
	Title(ctx context.Context, kind string, tmdbID int) (*tmdb.Full, error)
	Season(ctx context.Context, seriesID, season int) (*tmdb.SeasonFull, error)
	// Find is the TMDB ID an IMDb or TVDB ID names.
	Find(ctx context.Context, kind, source, id string) (int, error)
	Person(ctx context.Context, id int) (*tmdb.PersonFull, error)
	Release(ctx context.Context, kind string, tmdbID int) (*tmdb.Release, error)
	Search(ctx context.Context, query string) ([]tmdb.Title, []tmdb.PersonHit, error)
}

// Titles keeps what TMDB says about every film and series the library has,
// in Overfin's database: its page (name, year, overview, genres, runtime,
// cast), its images, and each of its seasons with their episodes, by title
// key, refreshed in the background within TMDB's six months. Everything
// people see about a title comes from here, never from Jellyfin.
type Titles struct {
	// Source is the current source, nil when there's none.
	Source func() TitleSource
	Store  *store.Store
	Logger *slog.Logger
	// Every is how often it looks for titles to fetch; Pause, how long it
	// waits between two requests, to go easy on TMDB.
	Every time.Duration
	Pause time.Duration

	wake chan struct{}
	now  func() time.Time
}

// TitlesFresh is how long a title's data stays before it's fetched again.
const TitlesFresh = 120 * 24 * time.Hour

// NewTitles returns the titles' keeper.
func NewTitles(source func() TitleSource, st *store.Store, logger *slog.Logger, every, pause time.Duration) *Titles {
	return &Titles{Source: source, Store: st, Logger: logger, Every: every, Pause: pause, wake: make(chan struct{}, 1)}
}

func (a *Titles) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// Wake asks for a pass now, for example after TMDB's language changed.
func (a *Titles) Wake() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Run makes a pass now, then every Every and whenever woken, until ctx
// ends.
func (a *Titles) Run(ctx context.Context) {
	for {
		if err := a.Once(ctx); err != nil && ctx.Err() == nil {
			a.Logger.Error("catalog: fetching titles failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-time.After(a.Every):
		}
	}
}

// Once deletes what's too old to keep, then fetches every library title's
// data not fetched for a while. A title TMDB fails on is left for the next pass;
// ten failures in a row stop this one.
func (a *Titles) Once(ctx context.Context) error {
	if err := a.Store.PurgeArtwork(ctx, a.clock().Add(-Kept)); err != nil {
		return err
	}
	if err := a.Store.PurgeSeasons(ctx, a.clock().Add(-Kept)); err != nil {
		return err
	}
	if err := a.Store.PurgePeople(ctx, a.clock().Add(-Kept)); err != nil {
		return err
	}
	src := a.Source()
	if src == nil {
		return nil
	}
	if err := a.Store.FillSearchNames(ctx); err != nil {
		return err
	}
	if err := a.find(ctx, src); err != nil {
		return err
	}
	fetched, streak := 0, 0
	failed := map[string]bool{}
	defer func() {
		if fetched > 0 {
			a.Logger.Info("catalog: titles fetched from TMDB", "titles", fetched)
		}
		if len(failed) > 0 {
			a.Logger.Warn("catalog: some titles couldn't be fetched from TMDB; tried again next time", "titles", len(failed))
		}
	}()
	for {
		todo, err := a.Store.ArtworkToFetch(ctx, a.clock().Add(-TitlesFresh), 100+len(failed))
		if err != nil {
			return err
		}
		progress := false
		for _, t := range todo {
			if failed[t.Key()] {
				continue
			}
			progress = true
			images, err := a.fetch(ctx, src, t)
			switch {
			case errors.Is(err, tmdb.ErrUnauthorized), ctx.Err() != nil:
				return err
			case err != nil:
				failed[t.Key()] = true
				if streak++; streak == 10 {
					return err
				}
				continue
			}
			streak = 0
			if err := a.Store.SetArtwork(ctx, t, images, a.clock()); err != nil {
				return err
			}
			fetched++
		}
		if !progress {
			return nil
		}
	}
}

// fetch reads and keeps a library title's page and seasons, and returns its
// images, its seasons' and episodes' included, by title key.
func (a *Titles) fetch(ctx context.Context, src TitleSource, t store.ArtworkTarget) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	id, err := strconv.Atoi(t.TMDB)
	if err != nil {
		return out, nil
	}
	seasons, err := a.page(ctx, src, t.Kind, id, out)
	if errors.Is(err, tmdb.ErrNotFound) {
		return out, nil // TMDB doesn't know it: nothing to show but its file
	}
	if err != nil {
		return nil, err
	}
	if err := a.pause(ctx); err != nil {
		return nil, err
	}
	// Every season TMDB lists, and any other the library has.
	for _, n := range t.Seasons {
		if !slices.Contains(seasons, n) {
			seasons = append(seasons, n)
		}
	}
	for _, n := range seasons {
		err := a.season(ctx, src, id, n, out)
		if errors.Is(err, tmdb.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := a.pause(ctx); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// page reads and keeps a film's or a series' page, adds its images to
// images, and returns the seasons TMDB lists.
func (a *Titles) page(ctx context.Context, src TitleSource, kind string, id int, images map[string]map[string]string) ([]int, error) {
	full, err := src.Title(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	det := store.TitleDetails{Genres: full.Genres, Runtime: full.Runtime, Tagline: full.Tagline}
	for _, c := range full.Cast {
		cm := store.CastMember{Name: c.Name, Character: c.Character, Profile: c.Profile}
		if c.ID > 0 {
			cm.TMDB = strconv.Itoa(c.ID)
		}
		det.Cast = append(det.Cast, cm)
	}
	var seasons []int
	for _, se := range full.Seasons {
		det.Seasons = append(det.Seasons, store.SeasonInfo{Number: se.Number, Name: se.Name, EpisodeCount: se.EpisodeCount})
		seasons = append(seasons, se.Number)
	}
	raw, err := json.Marshal(det)
	if err != nil {
		return nil, err
	}
	title := store.Title{Kind: kind, TMDB: strconv.Itoa(id), Name: full.Name, Year: full.Year, Overview: full.Overview,
		Poster: full.Artwork.Poster, Backdrop: full.Artwork.Backdrop, ReleaseDate: full.ReleaseDate, Rating: full.Rating}
	if err := a.Store.SetTitleDetails(ctx, title, raw, a.clock()); err != nil {
		return nil, err
	}
	images[kind+"/tmdb/"+title.TMDB] = map[string]string{"poster": full.Artwork.Poster, "backdrop": full.Artwork.Backdrop, "logo": full.Artwork.Logo}
	return seasons, nil
}

// season reads and keeps one of a series' seasons with its episodes, and
// adds their images to images.
func (a *Titles) season(ctx context.Context, src TitleSource, series, n int, images map[string]map[string]string) error {
	season, err := src.Season(ctx, series, n)
	if err != nil {
		return err
	}
	kept := store.Season{Number: n, Name: season.Name, Overview: season.Overview, AirDate: season.AirDate, Poster: season.Poster}
	sk := "series/tmdb/" + strconv.Itoa(series) + "/s" + strconv.Itoa(n)
	images[sk] = map[string]string{"poster": season.Poster}
	for _, e := range season.Episodes {
		kept.Episodes = append(kept.Episodes, store.Episode{Number: e.Number, Name: e.Name, Overview: e.Overview,
			AirDate: e.AirDate, Runtime: e.Runtime, Still: e.Still})
		images[sk+"/e"+strconv.Itoa(e.Number)] = map[string]string{"still": e.Still}
	}
	return a.Store.SetSeason(ctx, strconv.Itoa(series), kept, a.clock())
}

// FetchTitle reads and keeps, now, a film's or a series' page (without its
// seasons): for a title someone opens that the catalog doesn't describe
// yet. Its images are kept too when no library has it; for those a library
// has, the next pass brings them.
func (a *Titles) FetchTitle(ctx context.Context, kind, tmdbID string) error {
	src := a.Source()
	id, err := strconv.Atoi(tmdbID)
	if src == nil || err != nil {
		return tmdb.ErrNotFound
	}
	images := map[string]map[string]string{}
	if _, err := a.page(ctx, src, kind, id, images); err != nil {
		return err
	}
	inLibrary, err := a.Store.InLibrary(ctx, kind, tmdbID)
	if err != nil || inLibrary {
		return err
	}
	return a.Store.SetArtwork(ctx, store.ArtworkTarget{Kind: kind, TMDB: tmdbID}, images, a.clock())
}

// FetchSeason reads and keeps, now, one of a series' seasons: for a season
// someone browses that the catalog doesn't have yet.
func (a *Titles) FetchSeason(ctx context.Context, seriesID string, n int) error {
	src := a.Source()
	id, err := strconv.Atoi(seriesID)
	if src == nil || err != nil {
		return tmdb.ErrNotFound
	}
	return a.season(ctx, src, id, n, map[string]map[string]string{})
}

// find looks for the library's films and series known only by an IMDb or
// TVDB ID on TMDB, so they get its data too.
func (a *Titles) find(ctx context.Context, src TitleSource) error {
	todo, err := a.Store.TitlesToFind(ctx, a.clock().Add(-TitlesFresh), 200)
	if err != nil {
		return err
	}
	found := 0
	for _, t := range todo {
		id, err := src.Find(ctx, t.Kind, t.Source, t.External)
		switch {
		case errors.Is(err, tmdb.ErrNotFound):
			err = a.Store.SetFound(ctx, t, "", a.clock())
		case err != nil:
			return err
		default:
			found++
			err = a.Store.SetFound(ctx, t, strconv.Itoa(id), a.clock())
		}
		if err != nil {
			return err
		}
		if err := a.pause(ctx); err != nil {
			return err
		}
	}
	if found > 0 {
		a.Logger.Info("catalog: titles found on TMDB by their IMDb or TVDB ID", "titles", found)
	}
	return nil
}

func (a *Titles) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(a.Pause):
		return nil
	}
}

// FetchPerson reads and keeps, now, a person and their credits' titles:
// for a person someone opens.
func (a *Titles) FetchPerson(ctx context.Context, tmdbID string) error {
	src := a.Source()
	id, err := strconv.Atoi(tmdbID)
	if src == nil || err != nil {
		return tmdb.ErrNotFound
	}
	full, err := src.Person(ctx, id)
	if err != nil {
		return err
	}
	p := store.Person{TMDB: tmdbID, Name: full.Name, Biography: full.Biography, Birthday: full.Birthday,
		Deathday: full.Deathday, Birthplace: full.Birthplace, Department: full.Department, Profile: full.Profile}
	var titles []store.Title
	for _, c := range full.Credits {
		t := c.Title
		key := store.Title{Kind: t.Kind, TMDB: strconv.Itoa(t.TMDBID), Name: t.Name, Year: t.Year, Overview: t.Overview,
			Poster: t.Poster, Backdrop: t.Backdrop}
		titles = append(titles, key)
		p.Credits = append(p.Credits, store.PersonCredit{Kind: t.Kind, TMDB: key.TMDB, Role: c.Role, Crew: c.Crew})
	}
	return a.Store.SetPerson(ctx, p, titles, a.clock())
}

// Search asks TMDB for films, series and people matching query, keeps the
// titles (and the people, for their photos), and fetches now the release
// of those whose isn't known yet, so whether they're out can be judged.
func (a *Titles) Search(ctx context.Context, query string) ([]store.Title, []store.PersonHit, error) {
	src := a.Source()
	if src == nil {
		return nil, nil, tmdb.ErrNotFound
	}
	found, people, err := src.Search(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	now := a.clock()
	titles := make([]store.Title, 0, len(found))
	for _, t := range found {
		titles = append(titles, store.Title{Kind: t.Kind, TMDB: strconv.Itoa(t.TMDBID), Name: t.Name, Year: t.Year,
			Overview: t.Overview, Poster: t.Poster, Backdrop: t.Backdrop})
	}
	if err := a.Store.PutTitles(ctx, titles, now); err != nil {
		return nil, nil, err
	}
	for i, t := range titles {
		if i == 10 {
			break // the rest at the ratings keeper's next pass
		}
		_, _, known, err := a.Store.TitleRelease(ctx, t.Kind, t.TMDB)
		if err != nil {
			return nil, nil, err
		}
		if known {
			continue
		}
		r, err := src.Release(ctx, t.Kind, found[i].TMDBID)
		if errors.Is(err, tmdb.ErrNotFound) {
			r, err = &tmdb.Release{}, nil
		}
		if err != nil {
			return nil, nil, err
		}
		if err := a.Store.SetTitleRatings(ctx, store.RatedTitle{Kind: t.Kind, TMDB: t.TMDB}, r.Ratings, r.Released, r.AtHome, now); err != nil {
			return nil, nil, err
		}
	}
	hits := make([]store.PersonHit, 0, len(people))
	for _, p := range people {
		hits = append(hits, store.PersonHit{TMDB: strconv.Itoa(p.ID), Name: p.Name, Profile: p.Profile, Department: p.Department})
	}
	return titles, hits, a.Store.PutPeopleHits(ctx, hits, now)
}
