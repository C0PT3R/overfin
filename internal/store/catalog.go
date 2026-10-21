package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- Jellyfin service account ---------------------------------------------------

// The service account: an ordinary Jellyfin user of Overfin's own, never an
// administrator, that reads the whole library for the catalog. Its token is
// sealed, bound to its row.
const serviceTokenContext = "jellyfin service token"

// ServiceAccount is Overfin's Jellyfin service account.
type ServiceAccount struct {
	UserID   string
	Username string
	Token    string
}

// ErrServiceToken: the service account's token was sealed with another
// secret key (one that was lost). Service then returns the account without
// its token, for a new one.
var ErrServiceToken = errors.New("store: the service account's token can't be read with this secret key")

// Service returns the service account, or nil before it's made.
func (s *Store) Service(ctx context.Context) (*ServiceAccount, error) {
	var raw, sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'service-account'`).Scan(&raw)
	if err == nil {
		err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'service-token'`).Scan(&sealed)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a ServiceAccount
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("store: the service account: %w", err)
	}
	token, err := s.box.Open(sealed, serviceTokenContext)
	if err != nil {
		return &a, ErrServiceToken
	}
	a.Token = string(token)
	return &a, nil
}

// SetService saves the service account, its token sealed.
func (s *Store) SetService(ctx context.Context, a ServiceAccount) error {
	raw, err := json.Marshal(struct{ UserID, Username string }{a.UserID, a.Username})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, value := range map[string][]byte{
		"service-account": raw,
		"service-token":   s.box.Seal([]byte(a.Token), serviceTokenContext),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- Catalog --------------------------------------------------------------------

// CatalogItem is one title of the library, as the catalog indexes it.
type CatalogItem struct {
	ID            string
	Kind          string // movie, series, season, episode
	Name          string
	SortName      string
	Year          int // 0 when unknown
	SeriesID      string
	SeasonID      string
	SeasonNumber  *int
	EpisodeNumber *int
	TMDB          string
	IMDb          string
	TVDB          string
	Rating        string
	PrimaryTag    string
	BackdropTag   string
	ThumbTag      string
	// For listings: the library it's in (empty: unchanged), when it was
	// added, its release date (YYYY-MM-DD), its rating score, its genres
	// and studios.
	LibraryID       string
	AddedAt         time.Time
	PremiereDate    string
	CommunityRating *float64
	Genres          []string
	Studios         []string
}

// PutCatalog adds or updates titles, marking them seen by the full pass
// pass (the current one, for changes read between passes).
func (s *Store) PutCatalog(ctx context.Context, items []CatalogItem, pass int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO catalog_items (id, kind, name, sort_name, year,
		series_id, season_id, season_number, episode_number, tmdb, imdb, tvdb, rating,
		primary_tag, backdrop_tag, thumb_tag, seen, library_id, added_at, premiere_date, community_rating)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, name = excluded.name,
			sort_name = excluded.sort_name, year = excluded.year, series_id = excluded.series_id,
			season_id = excluded.season_id, season_number = excluded.season_number,
			episode_number = excluded.episode_number, tmdb = excluded.tmdb, imdb = excluded.imdb,
			tvdb = excluded.tvdb, rating = excluded.rating, primary_tag = excluded.primary_tag,
			backdrop_tag = excluded.backdrop_tag, thumb_tag = excluded.thumb_tag, seen = excluded.seen,
			library_id = coalesce(excluded.library_id, library_id), added_at = excluded.added_at,
			premiere_date = excluded.premiere_date, community_rating = excluded.community_rating`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, it := range items {
		var added any
		if !it.AddedAt.IsZero() {
			added = it.AddedAt.Unix()
		}
		if _, err := stmt.ExecContext(ctx, it.ID, it.Kind, it.Name, it.SortName, nullInt(it.Year),
			nullString(it.SeriesID), nullString(it.SeasonID), it.SeasonNumber, it.EpisodeNumber,
			nullString(it.TMDB), nullString(it.IMDb), nullString(it.TVDB), it.Rating,
			nullString(it.PrimaryTag), nullString(it.BackdropTag), nullString(it.ThumbTag), pass,
			nullString(it.LibraryID), added, nullString(it.PremiereDate), it.CommunityRating); err != nil {
			return err
		}
		// Genres and studios, replaced as a whole.
		for table, names := range map[string][]string{"catalog_genres": it.Genres, "catalog_studios": it.Studios} {
			column := map[string]string{"catalog_genres": "genre", "catalog_studios": "studio"}[table]
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE item_id = ?`, it.ID); err != nil {
				return err
			}
			for _, n := range names {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO `+table+` (item_id, `+column+`) VALUES (?, ?)`, it.ID, n); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

// UnseenCatalog lists the titles the full pass pass didn't see: gone from
// the library, or missed, which the caller checks before deleting them.
func (s *Store) UnseenCatalog(ctx context.Context, pass int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM catalog_items WHERE seen < ?`, pass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteCatalog removes titles from the index.
func (s *Store) DeleteCatalog(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_items WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CatalogCounts counts the indexed titles, by kind.
func (s *Store) CatalogCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, count(*) FROM catalog_items GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// CatalogUnmatched counts, by kind, the titles that can't be matched to
// TMDB later: films and series without a TMDB ID, seasons without their
// number, episodes without their season or episode number (episodes and
// seasons are matched by their show's ID and their numbers).
func (s *Store) CatalogUnmatched(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, count(*) FROM catalog_items
		WHERE (kind IN ('movie', 'series') AND tmdb IS NULL)
		   OR (kind = 'season' AND season_number IS NULL)
		   OR (kind = 'episode' AND (season_number IS NULL OR episode_number IS NULL))
		GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// CatalogState is where the catalog's sync stands, kept across restarts.
type CatalogState struct {
	// Pass counts full passes; titles carry the one that last saw them.
	Pass int64 `json:"pass"`
	// FullAt is when the last full pass started; ChangesAt, when the last
	// read of changes started (changes are read since then).
	FullAt    time.Time `json:"fullAt"`
	ChangesAt time.Time `json:"changesAt"`
}

// CatalogState returns the sync's state; zero before the first pass.
func (s *Store) CatalogState(ctx context.Context) (CatalogState, error) {
	var st CatalogState
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'catalog-state'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}

// SetCatalogState saves the sync's state.
func (s *Store) SetCatalogState(ctx context.Context, st CatalogState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('catalog-state', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, raw)
	return err
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// ---- Seerr ----------------------------------------------------------------------

// Seerr, as the control panel saved it: its address as is, its API key
// sealed, bound to its row.
const seerrKeyContext = "seerr api key"

// Seerr returns Seerr's address and API key, and false when none is saved.
func (s *Store) Seerr(ctx context.Context) (url, key string, ok bool, err error) {
	var u, sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'seerr-url'`).Scan(&u)
	if err == nil {
		err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'seerr-key'`).Scan(&sealed)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	k, err := s.box.Open(sealed, seerrKeyContext)
	if err != nil {
		return "", "", false, fmt.Errorf("store: Seerr's API key can't be read with this secret key: %w", err)
	}
	return string(u), string(k), true, nil
}

// SetSeerr saves Seerr's address and API key.
func (s *Store) SetSeerr(ctx context.Context, url, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, value := range map[string][]byte{
		"seerr-url": []byte(url),
		"seerr-key": s.box.Seal([]byte(key), seerrKeyContext),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ForgetSeerr removes Seerr's settings: Overfin no longer uses it.
func (s *Store) ForgetSeerr(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name IN ('seerr-url', 'seerr-key')`)
	return err
}

// CatalogByTMDB finds library titles of a kind (movie, series) by their
// TMDB IDs: TMDB ID → item ID, for those the library has.
func (s *Store) CatalogByTMDB(ctx context.Context, kind string, tmdbIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, tmdb := range tmdbIDs {
		var id string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM catalog_items WHERE kind = ? AND tmdb = ? ORDER BY id LIMIT 1`,
			kind, tmdb).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[tmdb] = id
	}
	return out, nil
}

// CatalogEpisodes counts a series' episodes in the library, by season
// number.
func (s *Store) CatalogEpisodes(ctx context.Context, seriesID string) (map[int]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT season_number, count(*) FROM catalog_items
		WHERE kind = 'episode' AND series_id = ? AND season_number IS NOT NULL GROUP BY season_number`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var season, n int
		if err := rows.Scan(&season, &n); err != nil {
			return nil, err
		}
		out[season] = n
	}
	return out, rows.Err()
}

// ListQuery is a library listing answered from the index.
type ListQuery struct {
	LibraryID string
	// ParentID, when set, lists a series' seasons or episodes, or a
	// season's episodes.
	ParentID string
	Kinds    []string // index kinds; empty: any
	Genres   []string // any of them
	Studios  []string // any of them
	YearFrom int      // 0: no bound
	YearTo   int
	// ProfileID, with Played, Favorite or InProgress, filters by what that
	// profile did with the titles.
	ProfileID  string
	Played     *bool
	Favorite   *bool
	InProgress bool
	// Sort is "name", "premiereDate", "addedAt" or "communityRating".
	Sort       string
	Descending bool
	Offset     int
	Limit      int
}

var listOrder = map[string]string{
	"name":            "sort_name COLLATE NOCASE",
	"premiereDate":    "premiere_date",
	"addedAt":         "added_at",
	"communityRating": "community_rating",
}

// ListCatalog returns the item IDs of a listing's page, in order. What's
// missing a sort value comes last, whichever the order.
func (s *Store) ListCatalog(ctx context.Context, q ListQuery) ([]string, error) {
	order, ok := listOrder[q.Sort]
	if !ok {
		return nil, fmt.Errorf("store: unknown sort %q", q.Sort)
	}
	where := []string{"1 = 1"}
	var args []any
	if q.LibraryID != "" {
		where, args = append(where, "library_id = ?"), append(args, q.LibraryID)
	}
	if q.ParentID != "" {
		where, args = append(where, "(series_id = ? OR season_id = ?)"), append(args, q.ParentID, q.ParentID)
	}
	in := func(column string, values []string) {
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", ")
		where = append(where, column+" IN ("+marks+")")
		for _, v := range values {
			args = append(args, v)
		}
	}
	if len(q.Kinds) > 0 {
		in("kind", q.Kinds)
	}
	for table, values := range map[string][]string{"catalog_genres": q.Genres, "catalog_studios": q.Studios} {
		if len(values) == 0 {
			continue
		}
		column := map[string]string{"catalog_genres": "genre", "catalog_studios": "studio"}[table]
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", ")
		where = append(where, "EXISTS (SELECT 1 FROM "+table+" x WHERE x.item_id = catalog_items.id AND x."+column+" IN ("+marks+"))")
		for _, v := range values {
			args = append(args, v)
		}
	}
	// What the profile did with the title, through its key.
	mine := func(cond string) string {
		return "EXISTS (SELECT 1 FROM profile_titles x WHERE x.profile_id = ? AND x.title = catalog_items.title_key AND " + cond + ")"
	}
	if q.Played != nil {
		cond := mine("x.played = 1")
		if !*q.Played {
			cond = "NOT " + cond
		}
		where, args = append(where, cond), append(args, q.ProfileID)
	}
	if q.Favorite != nil {
		cond := mine("x.favorite = 1")
		if !*q.Favorite {
			cond = "NOT " + cond
		}
		where, args = append(where, cond), append(args, q.ProfileID)
	}
	if q.InProgress {
		where, args = append(where, mine("x.position_ms > 0")), append(args, q.ProfileID)
	}
	if q.YearFrom > 0 {
		where, args = append(where, "year >= ?"), append(args, q.YearFrom)
	}
	if q.YearTo > 0 {
		where, args = append(where, "year <= ?"), append(args, q.YearTo)
	}
	dir := "ASC"
	if q.Descending {
		dir = "DESC"
	}
	query := `SELECT id FROM catalog_items WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + order + ` IS NULL, ` + order + ` ` + dir + `, sort_name COLLATE NOCASE, id LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
