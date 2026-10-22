package api

import (
	"net/url"
	"testing"
)

// What the index answers, and what goes to Jellyfin.
func TestIndexQuery(t *testing.T) {
	for query, want := range map[string][2]bool{ // [not imported, imported]
		"kind=movie":                            {true, true},
		"kind=movie&sort=addedAt&order=desc":    {true, true},
		"kind=series&genre=Drama&yearFrom=1990": {true, true},
		"kind=season&parent=item_0123":          {true, true},
		"":                                      {false, false}, // any kind
		"kind=musicAlbum":                       {false, false},
		"kind=movie&person=person_1":            {true, true},
		"kind=movie&sort=random":                {true, true},
		"kind=movie&sort=lastPlayed":            {false, true}, // the profile's own data
		// What the profile did: Overfin's once it has the profile's data.
		"kind=movie&played=false":        {false, true},
		"kind=episode&inProgress=true":   {false, true},
		"kind=series&favorite=true":      {false, true},
		"kind=series&played=true":        {false, true},  // every episode
		"kind=movie,season&inProgress=1": {false, false}, // "movie,season" isn't one kind
	} {
		q, _ := url.ParseQuery(query)
		for i, imported := range []bool{false, true} {
			if _, got := indexQuery(q, imported); got != want[i] {
				t.Errorf("%q, imported %v: %v, want %v", query, imported, got, want[i])
			}
		}
	}
}
