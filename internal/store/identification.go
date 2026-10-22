package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// LibraryTitle is a film or a series of the libraries as the control
// panel's Identification shows it: what the file is called, and the TMDB
// title it's taken for.
type LibraryTitle struct {
	ItemID    string
	Kind      string // movie, series
	LibraryID string
	// Name and Year are Jellyfin's; FolderName and FileName, the last
	// names of where it is; RuntimeMs, a film's length (0: unknown).
	Name                 string
	Year                 int
	FolderName, FileName string
	RuntimeMs            int64
	// TMDB is the TMDB ID it's taken for ("" for none); JellyfinTMDB,
	// Jellyfin's; Overridden, an admin chose TMDB.
	TMDB, JellyfinTMDB string
	Overridden         bool
	// The TMDB title's name, year and details, when kept.
	TMDBName string
	TMDBYear int
	Details  TitleDetails
	// Episodes counts a series' episodes the libraries have, by season.
	Episodes map[int]int
}

// LibraryTitles lists every film and series of the libraries, for
// Identification.
func (s *Store) LibraryTitles(ctx context.Context) ([]LibraryTitle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id, i.kind, coalesce(i.library_id, ''), i.name, coalesce(i.year, 0),
			coalesce(i.folder_name, ''), coalesce(i.file_name, ''), coalesce(i.runtime_ms, 0),
			coalesce(i.tmdb, ''), coalesce(i.jellyfin_tmdb, ''), o.item_id IS NOT NULL,
			coalesce(t.name, ''), coalesce(t.year, 0), t.details
		FROM catalog_items i
		LEFT JOIN title_overrides o ON o.item_id = i.id
		LEFT JOIN catalog_titles t ON t.kind = i.kind AND t.tmdb = i.tmdb
		WHERE i.kind IN ('movie', 'series')
		ORDER BY i.sort_name`)
	if err != nil {
		return nil, err
	}
	var out []LibraryTitle
	bySeries := map[string]*LibraryTitle{}
	for rows.Next() {
		var t LibraryTitle
		var details sql.NullString
		if err := rows.Scan(&t.ItemID, &t.Kind, &t.LibraryID, &t.Name, &t.Year, &t.FolderName, &t.FileName, &t.RuntimeMs,
			&t.TMDB, &t.JellyfinTMDB, &t.Overridden, &t.TMDBName, &t.TMDBYear, &details); err != nil {
			rows.Close()
			return nil, err
		}
		if details.Valid {
			json.Unmarshal([]byte(details.String), &t.Details)
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Kind == "series" {
			bySeries[out[i].ItemID] = &out[i]
		}
	}
	counts, err := s.db.QueryContext(ctx, `SELECT series_id, season_number, count(DISTINCT episode_number) FROM catalog_items
		WHERE kind = 'episode' AND series_id IS NOT NULL AND season_number IS NOT NULL AND episode_number IS NOT NULL
		GROUP BY series_id, season_number`)
	if err != nil {
		return nil, err
	}
	defer counts.Close()
	for counts.Next() {
		var series string
		var season, n int
		if err := counts.Scan(&series, &season, &n); err != nil {
			return nil, err
		}
		if t := bySeries[series]; t != nil {
			if t.Episodes == nil {
				t.Episodes = map[int]int{}
			}
			t.Episodes[season] = n
		}
	}
	return out, counts.Err()
}
