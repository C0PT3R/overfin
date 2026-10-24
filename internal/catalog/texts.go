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

// Texts fetches titles' texts in the languages offered besides the
// default (whose texts Titles keeps): first those someone was shown
// without them, then, slowly, the whole library's, each refreshed within
// TitlesFresh. Only TMDB itself gives them.
type Texts struct {
	Store *store.Store
	// Source is TMDB answering in a language; nil when TMDB isn't the
	// source.
	Source func(language string) TitleSource
	Logger *slog.Logger
	// Pause is the wait between two of TMDB's answers; Idle, between two
	// titles of the library's slow pass.
	Pause, Idle time.Duration
	wants       chan want
	// tried are the slow pass's titles tried lately (language, kind, TMDB
	// ID), and when: one TMDB doesn't know, or that fails, waits a day, and
	// the others go on. Only Run touches it.
	tried map[want]time.Time
}

// triedFor is how long the slow pass leaves a title it tried.
const triedFor = 24 * time.Hour

// want is a title someone was shown without its texts in a language.
type want struct{ language, kind, tmdb string }

// NewTexts returns the texts' keeper.
func NewTexts(source func(language string) TitleSource, st *store.Store, logger *slog.Logger, pause, idle time.Duration) *Texts {
	return &Texts{Store: st, Source: source, Logger: logger, Pause: pause, Idle: idle, wants: make(chan want, 256)}
}

// Want asks for titles' texts in language soon: those of films and series,
// and of a season's or an episode's series. It never waits; when too many
// are asked for at once, the slow pass gets the others.
func (x *Texts) Want(language string, refs []store.TitleRef) {
	if x == nil || language == "" {
		return
	}
	seen := map[string]bool{}
	for _, r := range refs {
		tmdbID := r.TMDB()
		if tmdbID == "" {
			continue
		}
		kind := "series"
		if r.Kind == "movie" {
			kind = "movie"
		}
		if seen[kind+tmdbID] {
			continue
		}
		seen[kind+tmdbID] = true
		select {
		case x.wants <- want{language, kind, tmdbID}:
		default:
		}
	}
}

// Run fetches texts until ctx ends: those wanted first, else one library
// title every Idle.
func (x *Texts) Run(ctx context.Context) {
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case w := <-x.wants:
			err = x.fetch(ctx, w)
		case <-time.After(x.Idle):
			err = x.slowly(ctx)
		}
		if err != nil && ctx.Err() == nil {
			x.Logger.Warn("catalog: titles' texts couldn't be fetched from TMDB; tried again later", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
		}
	}
}

// slowly fetches the texts of one library title missing them, in the
// first offered language that has one.
func (x *Texts) slowly(ctx context.Context) error {
	offered, def, _, err := x.Store.Languages(ctx)
	if err != nil {
		return err
	}
	for _, language := range offered {
		if language == def {
			continue
		}
		todo, err := x.Store.TextsToFetch(ctx, language, time.Now().Add(-TitlesFresh), len(x.tried)+1)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, t := range todo {
			w := want{language, t.Kind, t.TMDB}
			if at, ok := x.tried[w]; ok && now.Sub(at) < triedFor {
				continue
			}
			if x.tried == nil {
				x.tried = map[want]time.Time{}
			}
			for k, at := range x.tried {
				if now.Sub(at) >= triedFor {
					delete(x.tried, k)
				}
			}
			x.tried[w] = now
			return x.fetch(ctx, w)
		}
	}
	return nil
}

// fetch reads and keeps a film's or a series' texts in a language, a
// series' seasons and episodes included, unless they're fresh or the
// language isn't offered besides the default.
func (x *Texts) fetch(ctx context.Context, w want) error {
	offered, def, _, err := x.Store.Languages(ctx)
	if err != nil {
		return err
	}
	if w.language == def || !slices.Contains(offered, w.language) {
		return nil
	}
	if fresh, err := x.Store.HasTexts(ctx, w.language, w.kind, w.tmdb, time.Now().Add(-TitlesFresh)); err != nil || fresh {
		return err
	}
	src := x.Source(w.language)
	id, err := strconv.Atoi(w.tmdb)
	if src == nil || err != nil {
		return nil
	}
	full, err := src.Title(ctx, w.kind, id)
	if errors.Is(err, tmdb.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	det := store.TitleDetails{Genres: full.Genres, Tagline: full.Tagline}
	for _, c := range full.Cast {
		det.Cast = append(det.Cast, store.CastMember{Name: c.Name, Character: c.Character})
	}
	for _, se := range full.Seasons {
		det.Seasons = append(det.Seasons, store.SeasonInfo{Number: se.Number, Name: se.Name})
	}
	raw, err := json.Marshal(det)
	if err != nil {
		return err
	}
	var numbers []int
	for _, se := range full.Seasons {
		numbers = append(numbers, se.Number)
	}
	var got map[int]*tmdb.SeasonFull
	if len(numbers) > 0 {
		if err := x.pause(ctx); err != nil {
			return err
		}
		if got, err = src.Seasons(ctx, id, numbers); err != nil && !errors.Is(err, tmdb.ErrNotFound) {
			return err
		}
	}
	for _, n := range numbers {
		season := got[n]
		if season == nil {
			continue
		}
		kept := store.Season{Number: n, Name: season.Name, Overview: season.Overview}
		for _, e := range season.Episodes {
			kept.Episodes = append(kept.Episodes, store.Episode{Number: e.Number, Name: e.Name, Overview: e.Overview})
		}
		if err := x.Store.SetSeasonTexts(ctx, w.language, w.tmdb, kept); err != nil {
			return err
		}
	}
	// The title's own texts last: kept, they say the whole is.
	title := store.Title{Kind: w.kind, TMDB: w.tmdb, Name: full.Name, Overview: full.Overview}
	if err := x.Store.SetTitleTexts(ctx, w.language, title, raw, time.Now()); err != nil {
		return err
	}
	return x.pause(ctx)
}

func (x *Texts) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(x.Pause):
		return nil
	}
}
