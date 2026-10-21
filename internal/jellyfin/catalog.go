package jellyfin

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CatalogTypes are the item types Overfin's catalog indexes, for now.
var CatalogTypes = []string{"Movie", "Series", "Season", "Episode"}

// CatalogQuery is one page of the whole library, as the service account
// sees it, for the catalog's index.
type CatalogQuery struct {
	StartIndex int
	Limit      int
	// Since, when set, asks only for items Jellyfin saved since then.
	Since time.Time
}

// CatalogPage lists every title of the library, page by page, in a stable
// order (by when they were added, then by name). No personal data is asked
// for: the service account has none worth reading.
func (c *Client) CatalogPage(ctx context.Context, a Auth, userID string, q CatalogQuery) (*ItemPage, error) {
	v := url.Values{
		"userId":                 {userID},
		"recursive":              {"true"},
		"includeItemTypes":       {strings.Join(CatalogTypes, ",")},
		"fields":                 {"ProviderIds,SortName,ParentId"},
		"enableImageTypes":       {"Primary,Backdrop,Thumb"},
		"imageTypeLimit":         {"1"},
		"enableUserData":         {"false"},
		"enableTotalRecordCount": {"true"},
		"sortBy":                 {"DateCreated,SortName"},
		"sortOrder":              {"Ascending"},
		"startIndex":             {strconv.Itoa(q.StartIndex)},
		"limit":                  {strconv.Itoa(q.Limit)},
	}
	if !q.Since.IsZero() {
		v.Set("minDateLastSaved", q.Since.UTC().Format(time.RFC3339))
	}
	var out ItemPage
	if err := c.get(ctx, "catalog", "/Items", a, v, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CatalogItems returns, among ids, the titles the service account still
// sees, with the catalog's fields: the others are gone.
func (c *Client) CatalogItems(ctx context.Context, a Auth, userID string, ids []string) ([]RawItem, error) {
	v := url.Values{
		"userId":           {userID},
		"ids":              {strings.Join(ids, ",")},
		"fields":           {"ProviderIds,SortName,ParentId"},
		"enableImageTypes": {"Primary,Backdrop,Thumb"},
		"imageTypeLimit":   {"1"},
		"enableUserData":   {"false"},
	}
	var out ItemPage
	if err := c.get(ctx, "catalog items", "/Items", a, v, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
