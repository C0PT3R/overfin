package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
)

// Titles, whether a library has them or not: what TMDB says about each,
// and the library's copies of it.

// TitleRef is a title key, parsed: movie/tmdb/949, series/tmdb/1399,
// series/tmdb/1399/s1, series/tmdb/1399/s1/e2 (TMDB's, IMDb's or TVDB's
// IDs), or item/<id> for a file no one identified.
type TitleRef struct {
	Kind    string // movie, series, season, episode, item
	Source  string // tmdb, imdb, tvdb; "" for an item
	ID      string // the external ID, or the item's own
	Season  int    // seasons and episodes
	Episode int    // episodes
}

var (
	titleKeyPattern = regexp.MustCompile(`^(movie|series)/(tmdb|imdb|tvdb)/([A-Za-z0-9]{1,20})(?:/s([0-9]{1,4})(?:/e([0-9]{1,5}))?)?$`)
	itemKeyPattern  = regexp.MustCompile(`^item/([0-9a-f]{32})$`)
)

// ParseTitleKey checks a title key.
func ParseTitleKey(key string) (TitleRef, bool) {
	if m := itemKeyPattern.FindStringSubmatch(key); m != nil {
		return TitleRef{Kind: "item", ID: m[1]}, true
	}
	m := titleKeyPattern.FindStringSubmatch(key)
	if m == nil || (m[1] == "movie" && m[4] != "") {
		return TitleRef{}, false
	}
	r := TitleRef{Kind: m[1], Source: m[2], ID: m[3]}
	if m[4] != "" {
		r.Kind, r.Season = "season", atoi(m[4])
	}
	if m[5] != "" {
		r.Kind, r.Episode = "episode", atoi(m[5])
	}
	return r, true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Key is the ref's title key.
func (r TitleRef) Key() string {
	switch r.Kind {
	case "item":
		return "item/" + r.ID
	case "movie":
		return "movie/" + r.Source + "/" + r.ID
	case "series":
		return "series/" + r.Source + "/" + r.ID
	case "season":
		return "series/" + r.Source + "/" + r.ID + "/s" + strconv.Itoa(r.Season)
	}
	return "series/" + r.Source + "/" + r.ID + "/s" + strconv.Itoa(r.Season) + "/e" + strconv.Itoa(r.Episode)
}

// Series is the series a season or an episode is of.
func (r TitleRef) Series() TitleRef { return TitleRef{Kind: "series", Source: r.Source, ID: r.ID} }

// SeasonOf is the season an episode is in.
func (r TitleRef) SeasonOf() TitleRef {
	return TitleRef{Kind: "season", Source: r.Source, ID: r.ID, Season: r.Season}
}

// TMDB is the TMDB ID of the title, or of its series; "" without one.
func (r TitleRef) TMDB() string {
	if r.Source == "tmdb" {
		return r.ID
	}
	return ""
}

// TitleInfo is a title as Overfin shows it: what TMDB says about it (never
// what Jellyfin does), and which copies the library has.
type TitleInfo struct {
	Ref      TitleRef
	Name     string
	Year     int
	AirDate  string // seasons and episodes: YYYY-MM-DD, or ""
	Overview string
	Details  TitleDetails // films and series
	// Images are TMDB paths: a season or an episode has its series'
	// backdrop and logo.
	Poster, Backdrop, Logo, Still string
	// SeriesName is a season's or an episode's series'.
	SeriesName string
	// Items are the library's copies, by Jellyfin ID: for a series or a
	// season, its own items (to check access), not its episodes.
	Items []string
	// Have and Aired count a series' or a season's episodes (specials
	// aside): those the library has, and those TMDB says have aired.
	Have, Aired int
	// Known: TMDB's data for it is kept.
	Known bool
	// ItemKind is what the index calls an unidentified file (movie,
	// episode…).
	ItemKind string
}

// TitleInfos returns the titles of keys (those that parse), as of today
// (YYYY-MM-DD, for what has aired).
func (s *Store) TitleInfos(ctx context.Context, keys []string, today string) (map[string]*TitleInfo, error) {
	out := map[string]*TitleInfo{}
	for _, k := range keys {
		if _, done := out[k]; done {
			continue
		}
		r, ok := ParseTitleKey(k)
		if !ok {
			continue
		}
		t, err := s.titleInfo(ctx, r, today)
		if err != nil {
			return nil, err
		}
		out[k] = t
	}
	return out, nil
}

func (s *Store) titleInfo(ctx context.Context, r TitleRef, today string) (*TitleInfo, error) {
	t := &TitleInfo{Ref: r}
	key := r.Key()
	var err error
	if t.Items, err = s.ids(ctx, `SELECT id FROM catalog_items WHERE title_key = ? ORDER BY id`, key); err != nil {
		return nil, err
	}
	tmdb := r.TMDB()
	switch {
	case tmdb != "" && (r.Kind == "movie" || r.Kind == "series"):
		if err := s.fillTitle(ctx, t, r.Kind, tmdb); err != nil {
			return nil, err
		}
	case tmdb != "" && r.Kind == "season":
		var series TitleInfo
		if err := s.fillTitle(ctx, &series, "series", tmdb); err != nil {
			return nil, err
		}
		t.SeriesName, t.Backdrop, t.Logo = series.Name, series.Backdrop, series.Logo
		var poster sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT name, overview, coalesce(air_date, ''), poster FROM catalog_seasons
			WHERE series = ? AND season = ?`, tmdb, r.Season).Scan(&t.Name, &t.Overview, &t.AirDate, &poster)
		switch {
		case err == nil:
			t.Known, t.Poster = true, poster.String
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
		if t.Poster == "" {
			t.Poster = series.Poster
		}
	case tmdb != "" && r.Kind == "episode":
		var series TitleInfo
		if err := s.fillTitle(ctx, &series, "series", tmdb); err != nil {
			return nil, err
		}
		t.SeriesName, t.Backdrop, t.Logo, t.Poster = series.Name, series.Backdrop, series.Logo, series.Poster
		var still sql.NullString
		var runtime sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT name, overview, coalesce(air_date, ''), runtime, still FROM catalog_episodes
			WHERE series = ? AND season = ? AND episode = ?`, tmdb, r.Season, r.Episode).
			Scan(&t.Name, &t.Overview, &t.AirDate, &runtime, &still)
		switch {
		case err == nil:
			t.Known, t.Still, t.Details.Runtime = true, still.String, int(runtime.Int64)
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	default:
		// Not on TMDB (yet): known by its file only.
		err := s.db.QueryRowContext(ctx, `SELECT name, coalesce(year, 0), kind FROM catalog_items WHERE title_key = ?
			ORDER BY id LIMIT 1`, key).Scan(&t.Name, &t.Year, &t.ItemKind)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if len(t.AirDate) >= 4 && t.Year == 0 {
		t.Year = atoi(t.AirDate[:4])
	}
	if r.Kind == "series" || r.Kind == "season" {
		if err := s.countEpisodes(ctx, t, today); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// fillTitle fills in a film's or a series' page and images.
func (s *Store) fillTitle(ctx context.Context, t *TitleInfo, kind, tmdb string) error {
	title, err := s.TitleByTMDB(ctx, kind, tmdb)
	if err != nil {
		return err
	}
	if title != nil {
		t.Name, t.Year, t.Overview, t.Poster, t.Backdrop = title.Name, title.Year, title.Overview, title.Poster, title.Backdrop
		if len(title.Details) > 0 {
			t.Known = true
			if err := json.Unmarshal(title.Details, &t.Details); err != nil {
				return err
			}
		}
	}
	// The keeper's images, picked in the panel's language, come first.
	art, err := s.Artwork(ctx, []string{kind + "/tmdb/" + tmdb})
	if err != nil {
		return err
	}
	if a := art[kind+"/tmdb/"+tmdb]; a != nil {
		t.Poster, t.Backdrop, t.Logo = or(a["poster"], t.Poster), or(a["backdrop"], t.Backdrop), a["logo"]
	}
	return nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// countEpisodes counts a series' or a season's episodes, specials aside:
// those the library has, and those that aired by today (from TMDB's
// seasons when they're kept, else from its series page).
func (s *Store) countEpisodes(ctx context.Context, t *TitleInfo, today string) error {
	prefix := t.Ref.Key() + "/"
	if t.Ref.Kind == "series" {
		prefix += "s"
	}
	err := s.db.QueryRowContext(ctx, `SELECT count(DISTINCT title_key) FROM catalog_items
		WHERE kind = 'episode' AND title_key LIKE ? ESCAPE '\' AND coalesce(season_number, 0) > 0`,
		likePrefix(prefix)).Scan(&t.Have)
	if err != nil {
		return err
	}
	tmdb := t.Ref.TMDB()
	if tmdb == "" {
		return nil
	}
	q := `SELECT count(*), coalesce(sum(air_date IS NOT NULL AND air_date <= ?), 0) FROM catalog_episodes
		WHERE series = ? AND season > 0`
	args := []any{today, tmdb}
	if t.Ref.Kind == "season" {
		q += ` AND season = ?`
		args = append(args, t.Ref.Season)
	}
	var listed int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&listed, &t.Aired); err != nil {
		return err
	}
	if listed == 0 && t.Ref.Kind == "series" {
		// No season kept yet: the series page's counts.
		for _, se := range t.Details.Seasons {
			if se.Number > 0 {
				t.Aired += se.EpisodeCount
			}
		}
	}
	return nil
}

// SeasonEpisodes lists the episodes of a series' season as titles: those
// TMDB lists and those the library has, by number.
func (s *Store) SeasonEpisodes(ctx context.Context, season TitleRef) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	add := func(q string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n int
			if err := rows.Scan(&n); err != nil {
				return err
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		return rows.Err()
	}
	if tmdb := season.TMDB(); tmdb != "" {
		if err := add(`SELECT episode FROM catalog_episodes WHERE series = ? AND season = ? ORDER BY episode`, tmdb, season.Season); err != nil {
			return nil, err
		}
	}
	if err := add(`SELECT DISTINCT episode_number FROM catalog_items WHERE kind = 'episode' AND title_key LIKE ? ESCAPE '\'
		AND episode_number IS NOT NULL ORDER BY episode_number`, likePrefix(season.Key()+"/e")); err != nil {
		return nil, err
	}
	slices.Sort(out)
	return out, nil
}

// SeriesSeasons lists a series' seasons by number: those TMDB lists and
// those the library has.
func (s *Store) SeriesSeasons(ctx context.Context, series TitleRef, listed []SeasonInfo) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, se := range listed {
		if !seen[se.Number] {
			seen[se.Number] = true
			out = append(out, se.Number)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT season_number FROM catalog_items WHERE kind IN ('season', 'episode')
		AND title_key LIKE ? ESCAPE '\' AND season_number IS NOT NULL`, likePrefix(series.Key()+"/s"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, rows.Err()
}

// InLibrary says whether a library has a film or a series (by TMDB ID).
func (s *Store) InLibrary(ctx context.Context, kind, tmdb string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_items WHERE kind = ? AND tmdb = ?)`, kind, tmdb).Scan(&ok)
	return ok, err
}

// ImageTitles lists the titles (by key) an image of TMDB's, at path, is
// one of: a title's poster, backdrop or logo, a season's poster, an
// episode's still. At most limit.
func (s *Store) ImageTitles(ctx context.Context, typ, path string, limit int) ([]string, error) {
	var q string
	switch typ {
	case "poster", "backdrop":
		q = `SELECT key FROM artwork WHERE type = ?1 AND path = ?2
			UNION SELECT kind || '/tmdb/' || tmdb FROM catalog_titles WHERE (?1 = 'poster' AND poster = ?2) OR (?1 = 'backdrop' AND backdrop = ?2)
			UNION SELECT 'series/tmdb/' || series || '/s' || season FROM catalog_seasons WHERE ?1 = 'poster' AND poster = ?2`
	case "logo":
		q = `SELECT key FROM artwork WHERE type = ?1 AND path = ?2`
	case "still":
		q = `SELECT key FROM artwork WHERE type = ?1 AND path = ?2
			UNION SELECT 'series/tmdb/' || series || '/s' || season || '/e' || episode FROM catalog_episodes WHERE still = ?2`
	default:
		return nil, nil
	}
	return s.ids(ctx, `SELECT key FROM (`+q+`) LIMIT ?3`, typ, path, limit)
}
