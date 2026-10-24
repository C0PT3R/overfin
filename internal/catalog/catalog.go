// Package catalog keeps Overfin's index of the library: every title, with
// its TMDB, IMDb and TVDB IDs, its rating and its image references, read
// through Overfin's Jellyfin service account and kept in Overfin's
// database. Nothing personal is read or kept.
//
// A full pass reads the whole library now and then, and is the only one that
// removes titles; in between, only what Jellyfin saved since the last read
// is read again.
package catalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
)

// Service is the Jellyfin service account the catalog reads with.
type Service interface {
	Auth(ctx context.Context) (jellyfin.Auth, string, error)
	// Refused tells it Jellyfin refused its token; it gets a new one.
	Refused(ctx context.Context) error
}

// Sync keeps the index in step with the library.
type Sync struct {
	Jellyfin *jellyfin.Client
	Store    *store.Store
	Service  Service
	Logger   *slog.Logger
	// ChangesEvery is how often changes are read while Jellyfin's socket
	// is out of reach (Watch); FullEvery, how often the whole library is.
	ChangesEvery time.Duration
	FullEvery    time.Duration

	mu      sync.Mutex
	lastErr string
	now     func() time.Time
	// watching: Jellyfin's socket is connected, and says what changes.
	watching atomic.Bool
	wake     chan struct{}
	// removed are the items Jellyfin said were removed, to check.
	removed map[string]bool
}

// wakeUp has Run read the changes now.
func (s *Sync) wakeUp() {
	select {
	case s.wakeCh() <- struct{}{}:
	default:
	}
}

func (s *Sync) wakeCh() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	return s.wake
}

// Watch listens on Jellyfin's socket as the service account until ctx
// ends: each library change has the index read the changes at once, so
// it doesn't ask Jellyfin every ChangesEvery. When the socket drops, the
// changes are read on reconnecting (the margin covers what was said in
// between), and the index polls meanwhile.
func (s *Sync) Watch(ctx context.Context) {
	wait := 10 * time.Second
	for ctx.Err() == nil {
		a, _, err := s.Service.Auth(ctx)
		if err == nil {
			err = s.Jellyfin.Watch(ctx, a, func() {
				s.watching.Store(true)
				wait = 10 * time.Second
				s.Logger.Info("catalog: listening to Jellyfin for library changes")
				s.wakeUp()
			}, s.changed)
		}
		was := s.watching.Swap(false)
		if ctx.Err() != nil {
			return
		}
		if jellyfin.IsStatus(err, http.StatusUnauthorized) {
			if err := s.Service.Refused(ctx); err != nil {
				s.Logger.Warn("catalog: renewing the service account failed", "err", err)
			}
		}
		if was {
			s.Logger.Warn("catalog: Jellyfin's socket dropped; reading changes every few minutes until it's back", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, 5*time.Minute)
	}
}

// changed notes a library change from Jellyfin, and wakes Run.
func (s *Sync) changed(lc jellyfin.LibraryChange) {
	if len(lc.ItemsRemoved) > 0 {
		s.mu.Lock()
		if s.removed == nil {
			s.removed = map[string]bool{}
		}
		for _, raw := range lc.ItemsRemoved {
			if id, err := jellyfin.NormalizeID(raw); err == nil && len(s.removed) < 100_000 {
				s.removed[id] = true
			}
		}
		s.mu.Unlock()
	}
	s.wakeUp()
}

// pageSize is how many titles one request reads. With their people, a
// page of 500 took Jellyfin longer than the client waits.
const pageSize = 100

// margin is how far back changes are read again, so nothing saved during
// the previous read is missed.
const margin = 2 * time.Minute

func (s *Sync) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Run keeps the index in step until ctx ends: a full pass first if the last
// one is old or there's none, then changes when Jellyfin's socket says
// (Watch; every ChangesEvery while it's out of reach), and a full pass
// every FullEvery.
func (s *Sync) Run(ctx context.Context) {
	// Title keys first, for items indexed before they existed.
	if err := s.Store.RefreshTitleKeys(ctx); err != nil && ctx.Err() == nil {
		s.Logger.Error("catalog: computing title keys failed", "err", err)
	}
	wake := s.wakeCh()
	for {
		s.once(ctx)
		every := s.ChangesEvery
		if s.watching.Load() {
			every = time.Hour // only to see whether a full pass is due
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(every):
		}
	}
}

func (s *Sync) once(ctx context.Context) {
	s.mu.Lock()
	removed := slices.Collect(maps.Keys(s.removed))
	s.removed = nil
	s.mu.Unlock()
	st, err := s.Store.CatalogState(ctx)
	if err == nil && len(removed) > 0 && st.Pass > 0 {
		var n int
		if n, err = s.confirmGone(ctx, removed, st.Pass); n > 0 {
			s.Logger.Info("catalog: titles removed", "titles", n)
		}
	}
	if err == nil {
		if st.Pass == 0 || s.clock().Sub(st.FullAt) >= s.FullEvery {
			err = s.Full(ctx)
		} else {
			err = s.Changes(ctx)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		s.Logger.Error("catalog: reading the library failed", "err", err)
		s.lastErr = err.Error()
	} else {
		s.lastErr = ""
	}
}

// Full reads the whole library, then removes the titles that are gone.
func (s *Sync) Full(ctx context.Context) error {
	st, err := s.Store.CatalogState(ctx)
	if err != nil {
		return err
	}
	start := s.clock()
	pass := st.Pass + 1
	n, err := s.read(ctx, time.Time{}, pass)
	if err != nil {
		return err
	}
	removed, err := s.sweep(ctx, pass)
	if err != nil {
		return err
	}
	if err := s.Store.RefreshTitleKeys(ctx); err != nil {
		return err
	}
	st.Pass, st.FullAt, st.ChangesAt = pass, start, start
	if err := s.Store.SetCatalogState(ctx, st); err != nil {
		return err
	}
	s.Logger.Info("catalog: the library was read", "titles", n, "removed", removed)
	return nil
}

// Changes reads what Jellyfin saved since the last read.
func (s *Sync) Changes(ctx context.Context) error {
	st, err := s.Store.CatalogState(ctx)
	if err != nil {
		return err
	}
	start := s.clock()
	n, err := s.read(ctx, st.ChangesAt.Add(-margin), st.Pass)
	if err != nil {
		return err
	}
	if n > 0 {
		if err := s.Store.RefreshTitleKeys(ctx); err != nil {
			return err
		}
	}
	st.ChangesAt = start
	if err := s.Store.SetCatalogState(ctx, st); err != nil {
		return err
	}
	if n > 0 {
		s.Logger.Info("catalog: changes read", "titles", n)
	}
	return nil
}

// read reads the library, library by library (everything, or what was
// saved since since), and stores it as seen by pass. It returns how many
// titles it read.
func (s *Sync) read(ctx context.Context, since time.Time, pass int64) (int, error) {
	var libs []jellyfin.RawItem
	if err := s.withService(ctx, func(a jellyfin.Auth, userID string) error {
		var err error
		libs, err = s.Jellyfin.Libraries(ctx, a, userID)
		return err
	}); err != nil {
		return 0, err
	}
	kept := make([]store.Library, 0, len(libs))
	for _, lib := range libs {
		if id, err := jellyfin.NormalizeID(lib.Id); err == nil {
			lib.Path = "" // never kept
			item, err := json.Marshal(lib)
			if err != nil {
				return 0, err
			}
			kept = append(kept, store.Library{ID: id, Item: item})
		}
	}
	if err := s.Store.SetLibraries(ctx, kept); err != nil {
		return 0, err
	}
	n := 0
	for _, lib := range libs {
		libID, err := jellyfin.NormalizeID(lib.Id)
		if err != nil {
			continue
		}
		for start := 0; ; start += pageSize {
			var page *jellyfin.ItemPage
			err := s.withService(ctx, func(a jellyfin.Auth, userID string) error {
				var err error
				page, err = s.Jellyfin.CatalogPage(ctx, a, userID,
					jellyfin.CatalogQuery{StartIndex: start, Limit: pageSize, Since: since, LibraryID: libID})
				return err
			})
			if err != nil {
				return n, err
			}
			var items []store.CatalogItem
			for _, raw := range page.Items {
				if it, ok := convert(raw); ok {
					it.LibraryID = libID
					items = append(items, it)
				}
			}
			if err := s.Store.PutCatalog(ctx, items, pass); err != nil {
				return n, err
			}
			n += len(items)
			if len(page.Items) < pageSize {
				break
			}
		}
	}
	return n, nil
}

// sweep removes the titles the full pass didn't see, after asking Jellyfin
// about them again: titles added while the pass ran shift its pages, and
// one missed that way must not be removed.
func (s *Sync) sweep(ctx context.Context, pass int64) (int, error) {
	unseen, err := s.Store.UnseenCatalog(ctx, pass)
	if err != nil {
		return 0, err
	}
	return s.confirmGone(ctx, unseen, pass)
}

// confirmGone asks Jellyfin about items thought gone, keeps those it still
// has (as seen by pass), and removes the others from the index.
func (s *Sync) confirmGone(ctx context.Context, ids []string, pass int64) (int, error) {
	removed := 0
	for batch := range slices.Chunk(ids, 100) {
		var found []jellyfin.RawItem
		err := s.withService(ctx, func(a jellyfin.Auth, userID string) error {
			var err error
			found, err = s.Jellyfin.CatalogItems(ctx, a, userID, batch)
			return err
		})
		if err != nil {
			return removed, err
		}
		var still []store.CatalogItem
		present := map[string]bool{}
		for _, raw := range found {
			if it, ok := convert(raw); ok {
				still = append(still, it)
				present[it.ID] = true
			}
		}
		if err := s.Store.PutCatalog(ctx, still, pass); err != nil {
			return removed, err
		}
		var gone []string
		for _, id := range batch {
			if !present[id] {
				gone = append(gone, id)
			}
		}
		if err := s.Store.DeleteCatalog(ctx, gone); err != nil {
			return removed, err
		}
		removed += len(gone)
	}
	return removed, nil
}

// withService runs fn with the service account, and once more with a new
// token if Jellyfin refused the first.
func (s *Sync) withService(ctx context.Context, fn func(jellyfin.Auth, string) error) error {
	a, userID, err := s.Service.Auth(ctx)
	if err != nil {
		return err
	}
	err = fn(a, userID)
	if !jellyfin.IsStatus(err, http.StatusUnauthorized) {
		return err
	}
	if err := s.Service.Refused(ctx); err != nil {
		return err
	}
	if a, userID, err = s.Service.Auth(ctx); err != nil {
		return err
	}
	return fn(a, userID)
}

// Status is where the index stands, for the control panel.
type Status struct {
	Titles map[string]int `json:"titles"`
	// Unmatched counts, by kind, the titles that can't be matched to TMDB
	// (store.CatalogUnmatched).
	Unmatched map[string]int `json:"unmatched"`
	FullAt    time.Time      `json:"fullAt"`
	ChangesAt time.Time      `json:"changesAt"`
	// Error is the last read's failure, if it failed.
	Error string `json:"error,omitempty"`
}

// Status says where the index stands.
func (s *Sync) Status(ctx context.Context) (Status, error) {
	st, err := s.Store.CatalogState(ctx)
	if err != nil {
		return Status{}, err
	}
	titles, err := s.Store.CatalogCounts(ctx)
	if err != nil {
		return Status{}, err
	}
	unmatched, err := s.Store.CatalogUnmatched(ctx)
	if err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Titles: titles, Unmatched: unmatched, FullAt: st.FullAt, ChangesAt: st.ChangesAt, Error: s.lastErr}, nil
}

// ---- From Jellyfin's items to the index -----------------------------------------

var kinds = map[string]string{"Movie": "movie", "Series": "series", "Season": "season", "Episode": "episode"}

// What an upstream ID may look like (SECURITY.md rule 7): anything else is
// left out.
var (
	tmdbID   = regexp.MustCompile(`^[0-9]{1,10}$`)
	imdbID   = regexp.MustCompile(`^tt[0-9]{1,10}$`)
	tvdbID   = regexp.MustCompile(`^[0-9]{1,10}$`)
	imageTag = regexp.MustCompile(`^[0-9A-Za-z]{1,64}$`)
)

// convert turns one of Jellyfin's items into a title of the index, its text
// made plain and its IDs checked. Items of other types, or with an ID that
// isn't one, are left out.
func convert(raw jellyfin.RawItem) (store.CatalogItem, bool) {
	kind, ok := kinds[raw.Type]
	if !ok {
		return store.CatalogItem{}, false
	}
	id, err := jellyfin.NormalizeID(raw.Id)
	if err != nil {
		return store.CatalogItem{}, false
	}
	it := store.CatalogItem{
		ID:       id,
		Kind:     kind,
		Name:     textclean.Plain(raw.Name, 300),
		SortName: textclean.Plain(raw.SortName, 300),
		Year:     raw.ProductionYear,
		Rating:   textclean.Plain(raw.OfficialRating, 32),
		TMDB:     matching(tmdbID, raw.ProviderIds["Tmdb"]),
		IMDb:     matching(imdbID, raw.ProviderIds["Imdb"]),
		TVDB:     matching(tvdbID, raw.ProviderIds["Tvdb"]),
	}
	if it.SortName == "" {
		it.SortName = it.Name
	}
	if kind == "season" || kind == "episode" {
		it.SeriesID, _ = jellyfin.NormalizeID(raw.SeriesId)
	}
	switch kind {
	case "season":
		it.SeasonNumber = raw.IndexNumber
	case "episode":
		it.SeasonID, _ = jellyfin.NormalizeID(raw.SeasonId)
		it.SeasonNumber, it.EpisodeNumber = raw.ParentIndexNumber, raw.IndexNumber
	}
	if t, err := time.Parse(time.RFC3339Nano, raw.DateCreated); err == nil {
		it.AddedAt = t
	}
	// What the file and its folder are named, for telling a misidentified
	// title (the control panel's Identification): a film's file and the
	// folder it's in, a series' folder. Never the whole path.
	switch kind {
	case "movie":
		dir, file := splitPath(raw.Path)
		_, it.FolderName = splitPath(dir)
		it.FileName = file
		if raw.RunTimeTicks > 0 {
			it.RuntimeMs = raw.RunTimeTicks / 10_000
		}
	case "series":
		_, it.FolderName = splitPath(raw.Path)
	}
	it.FolderName, it.FileName = textclean.Plain(it.FolderName, 255), textclean.Plain(it.FileName, 255)
	if len(raw.PremiereDate) >= 10 {
		if _, err := time.Parse("2006-01-02", raw.PremiereDate[:10]); err == nil {
			it.PremiereDate = raw.PremiereDate[:10]
		}
	}
	if r := raw.CommunityRating; r != nil && *r >= 0 && *r <= 10 {
		it.CommunityRating = r
	}
	for _, g := range raw.Genres {
		if name := textclean.Plain(g, 128); name != "" && len(it.Genres) < 20 {
			it.Genres = append(it.Genres, name)
		}
	}
	for _, st := range raw.Studios {
		if name := textclean.Plain(st.Name, 128); name != "" && len(it.Studios) < 20 {
			it.Studios = append(it.Studios, name)
		}
	}
	// Tags hide titles from profiles (SECURITY.md rule 28): every one is
	// kept, up to a bound no real title reaches.
	for _, tag := range raw.Tags {
		if name := textclean.Plain(tag, 128); name != "" && len(it.Tags) < maxTags {
			it.Tags = append(it.Tags, name)
		}
	}
	for _, p := range raw.People {
		if pid, err := jellyfin.NormalizeID(p.Id); err == nil && len(it.People) < 100 && !slices.Contains(it.People, pid) {
			it.People = append(it.People, pid)
		}
	}
	it.PrimaryTag = matching(imageTag, raw.ImageTags["Primary"])
	it.ThumbTag = matching(imageTag, raw.ImageTags["Thumb"])
	if len(raw.BackdropImageTags) > 0 {
		it.BackdropTag = matching(imageTag, raw.BackdropImageTags[0])
	}
	return it, true
}

func matching(re *regexp.Regexp, s string) string {
	if re.MatchString(s) {
		return s
	}
	return ""
}

// splitPath splits a path from Jellyfin's server (slashes or backslashes)
// into its folder and its last name.
func splitPath(p string) (dir, name string) {
	p = strings.TrimRight(p, `/\`)
	i := strings.LastIndexAny(p, `/\`)
	return p[:max(i, 0)], p[i+1:]
}

// maxTags is how many tags of a title the index keeps.
const maxTags = 500
