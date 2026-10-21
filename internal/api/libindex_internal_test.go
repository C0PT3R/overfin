package api

import (
	"net/url"
	"testing"

	"github.com/C0PT3R/overfin-server/internal/store"
)

// What the index answers, and what goes to Jellyfin.
func TestIndexQuery(t *testing.T) {
	standard, kids := &store.Profile{Kind: "standard"}, &store.Profile{Kind: "kids"}
	for query, want := range map[string]bool{
		"kind=movie":                            true,
		"kind=movie&sort=addedAt&order=desc":    true,
		"kind=series&genre=Drama&yearFrom=1990": true,
		"kind=season&parent=item_0123":          true,
		"":                                      false, // any kind
		"kind=musicAlbum":                       false,
		"kind=movie&played=false":               false,
		"kind=movie&favorite=true":              false,
		"kind=movie&inProgress=true":            false,
		"kind=movie&person=person_1":            false,
		"kind=movie&sort=random":                false,
		"kind=movie&sort=lastPlayed":            false,
	} {
		q, _ := url.ParseQuery(query)
		if _, got := indexQuery(q, standard); got != want {
			t.Errorf("%q: %v, want %v", query, got, want)
		}
		// Kids profiles: Jellyfin, always.
		if _, got := indexQuery(q, kids); got {
			t.Errorf("kids, %q: answered by the index", query)
		}
	}
}
