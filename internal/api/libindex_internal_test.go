package api

import (
	"net/url"
	"testing"

	"github.com/C0PT3R/overfin-server/internal/store"
)

// What the index answers, and what goes to Jellyfin.
func TestIndexQuery(t *testing.T) {
	standard, kids := &store.Profile{Kind: "standard"}, &store.Profile{Kind: "kids"}
	for query, want := range map[string][2]bool{ // [not imported, imported]
		"kind=movie":                            {true, true},
		"kind=movie&sort=addedAt&order=desc":    {true, true},
		"kind=series&genre=Drama&yearFrom=1990": {true, true},
		"kind=season&parent=item_0123":          {true, true},
		"":                                      {false, false}, // any kind
		"kind=musicAlbum":                       {false, false},
		"kind=movie&person=person_1":            {false, false},
		"kind=movie&sort=random":                {false, false},
		"kind=movie&sort=lastPlayed":            {false, false},
		// What the profile did: Overfin's once it has the profile's data.
		"kind=movie&played=false":        {false, true},
		"kind=episode&inProgress=true":   {false, true},
		"kind=series&favorite=true":      {false, true},
		"kind=series&played=true":        {false, false}, // every episode: not counted yet
		"kind=movie,season&inProgress=1": {false, false},
	} {
		q, _ := url.ParseQuery(query)
		for i, imported := range []bool{false, true} {
			if _, got := indexQuery(q, standard, imported); got != want[i] {
				t.Errorf("%q, imported %v: %v, want %v", query, imported, got, want[i])
			}
			// Kids profiles: Jellyfin, always.
			if _, got := indexQuery(q, kids, imported); got {
				t.Errorf("kids, %q: answered by the index", query)
			}
		}
	}
}
