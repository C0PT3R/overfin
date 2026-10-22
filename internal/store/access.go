package store

import (
	"context"
	"encoding/json"
	"strings"
)

// Access is what a profile may see of the library, when it's limited:
// titles within its ratings ceiling (kids profiles), in the libraries it
// gets, and without the tags hidden from it (SECURITY.md rule 28).
type Access struct {
	// Ceiling, when set, holds a kids profile to its ratings.
	Ceiling *Ceiling
	// Libraries, when not nil, are the only libraries it gets (by
	// Jellyfin ID); empty, it gets none.
	Libraries []string
	// BlockedTags are hidden from it, on a title or on its series or
	// season; matched regardless of case.
	BlockedTags []string
}

// AccessOf returns what a profile may see, or nil when it may see
// everything. country is the ratings country.
func AccessOf(p *Profile, country string) *Access {
	a := &Access{Libraries: p.Libraries, BlockedTags: p.BlockedTags}
	if p.Kind == "kids" {
		a.Ceiling = &Ceiling{Country: country, Movie: p.MovieCeiling, Series: p.SeriesCeiling}
	}
	if a.Ceiling == nil && a.Libraries == nil && len(a.BlockedTags) == 0 {
		return nil
	}
	return a
}

// GetsLibrary says whether the access allows a library.
func (a *Access) GetsLibrary(id string) bool {
	if a == nil || a.Libraries == nil {
		return true
	}
	for _, l := range a.Libraries {
		if l == id {
			return true
		}
	}
	return false
}

// Blocks says whether one of tags is hidden.
func (a *Access) Blocks(tags []string) bool {
	if a == nil {
		return false
	}
	for _, t := range tags {
		for _, b := range a.BlockedTags {
			if strings.EqualFold(t, b) {
				return true
			}
		}
	}
	return false
}

// itemAllowed is the access as SQL, for the catalog_items row named table.
func (a Access) itemAllowed(table string) (string, []any) {
	var conds []string
	var args []any
	if a.Ceiling != nil {
		c, ca := a.Ceiling.itemAllowed(table)
		conds, args = append(conds, c), append(args, ca...)
	}
	if a.Libraries != nil {
		if len(a.Libraries) == 0 {
			conds = append(conds, "0 = 1")
		} else {
			conds = append(conds, table+".library_id IN ("+marks(len(a.Libraries))+")")
			for _, l := range a.Libraries {
				args = append(args, l)
			}
		}
	}
	if len(a.BlockedTags) > 0 {
		conds = append(conds, `NOT EXISTS (SELECT 1 FROM catalog_tags t
			WHERE t.item_id IN (`+table+`.id, coalesce(`+table+`.series_id, ''), coalesce(`+table+`.season_id, ''))
			AND t.tag COLLATE NOCASE IN (`+marks(len(a.BlockedTags))+`))`)
		for _, t := range a.BlockedTags {
			args = append(args, t)
		}
	}
	if len(conds) == 0 {
		return "1 = 1", nil
	}
	return "(" + strings.Join(conds, " AND ") + ")", args
}

// AllowedItems says which of the library's items (by Jellyfin ID) the
// access allows. Items the index doesn't have aren't in the answer: the
// caller decides about them.
func (s *Store) AllowedItems(ctx context.Context, a Access, itemIDs []string) (allowed, indexed map[string]bool, err error) {
	allowed, indexed = map[string]bool{}, map[string]bool{}
	cond, args := a.itemAllowed("i")
	for start := 0; start < len(itemIDs); start += 500 {
		chunk := itemIDs[start:min(start+500, len(itemIDs))]
		q := `SELECT id, ` + cond + ` FROM catalog_items i WHERE id IN (` + marks(len(chunk)) + `)`
		ca := make([]any, 0, len(args)+len(chunk))
		ca = append(ca, args...)
		for _, id := range chunk {
			ca = append(ca, id)
		}
		rows, err := s.db.QueryContext(ctx, q, ca...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			var ok bool
			if err := rows.Scan(&id, &ok); err != nil {
				rows.Close()
				return nil, nil, err
			}
			indexed[id] = true
			if ok {
				allowed[id] = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return allowed, indexed, nil
}

// CatalogTags lists the library's tags, for choosing which to hide.
func (s *Store) CatalogTags(ctx context.Context) ([]string, error) {
	return s.ids(ctx, `SELECT tag FROM catalog_tags GROUP BY tag COLLATE NOCASE ORDER BY tag COLLATE NOCASE`)
}

// SetProfileAccess changes the libraries a profile gets (nil: every
// library) and the tags hidden from it, within its account.
func (s *Store) SetProfileAccess(ctx context.Context, accountID, profileID string, libraries, blockedTags []string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET libraries = ?, blocked_tags = ? WHERE id = ? AND account_id = ?`,
		jsonList(libraries), jsonList(blockedTags), profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// jsonList stores a list as JSON, nil as NULL.
func jsonList(l []string) any {
	if l == nil {
		return nil
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// scanList reads a list stored by jsonList.
func scanList(v *string) []string {
	if v == nil {
		return nil
	}
	out := []string{}
	_ = json.Unmarshal([]byte(*v), &out)
	return out
}
