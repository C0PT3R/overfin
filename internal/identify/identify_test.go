package identify

import (
	"slices"
	"testing"

	"github.com/overfin/server/internal/store"
)

func codes(rs []Reason) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

func TestParse(t *testing.T) {
	for in, want := range map[string]named{
		"Heat (1995)":                            {name: "Heat", year: 1995},
		"Heat (1995) [tmdbid-949]":               {name: "Heat", year: 1995, tmdb: "949"},
		"The.Candidate.2023.1080p.WEB.mkv":       {name: "The Candidate", year: 2023},
		"1917 (2019)":                            {name: "1917", year: 2019},
		"1917":                                   {name: "1917"},
		"2001 A Space Odyssey (1968)":            {name: "2001 A Space Odyssey", year: 1968},
		"19-2":                                   {name: "19-2"},
		"Blade Runner 2049 (2017) {tmdb-335984}": {name: "Blade Runner 2049", year: 2017, tmdb: "335984"},
	} {
		if got := parse(in); got != want {
			t.Errorf("%q: %+v, want %+v", in, got, want)
		}
	}
}

// The cases found on a real library: each wrong one is told, and right
// ones aren't, even named in another language than TMDB's answers.
func TestReasons(t *testing.T) {
	film := func(folder, name string, year int, d store.TitleDetails) store.LibraryTitle {
		return store.LibraryTitle{Kind: "movie", FolderName: folder, TMDB: "1", TMDBName: name, TMDBYear: year, Details: d}
	}
	cases := []struct {
		about string
		t     store.LibraryTitle
		want  []string
	}{
		{"taken for another film", film("The Swedish Connection (2026)", "L'Odyssée", 2026,
			store.TitleDetails{OriginalName: "The Odyssey"}), []string{"name"}},
		{"right, in French on TMDB, English here", film("Spirited Away (2001)", "Le Voyage de Chihiro", 2001,
			store.TitleDetails{OriginalName: "千と千尋の神隠し", Names: []string{"Spirited Away"}}), nil},
		{"a remake of the same name", film("Trick 'r Treat (2025)", "Trick 'r Treat", 2007, store.TitleDetails{}), []string{"year"}},
		{"a subtitle left out", film("Dune Part Two (2024)", "Dune : Deuxième partie", 2024,
			store.TitleDetails{OriginalName: "Dune: Part Two"}), nil},
		{"no TMDB title", store.LibraryTitle{Kind: "movie", FolderName: "Home movie"}, []string{"no-tmdb"}},
		{"its folder names another", film("Heat (1995) [tmdbid-949]", "Heat", 1995, store.TitleDetails{}), []string{"tmdb-tag"}},
		{"chosen by an admin", store.LibraryTitle{Kind: "movie", FolderName: "Anything", TMDB: "1", TMDBName: "Else", Overridden: true}, nil},
		{"far longer than TMDB says", store.LibraryTitle{Kind: "movie", FolderName: "Heat (1995)", TMDB: "1", TMDBName: "Heat",
			TMDBYear: 1995, RuntimeMs: 24 * 60_000, Details: store.TitleDetails{Runtime: 170}}, []string{"runtime"}},
		{"the remake of a series: more seasons here", store.LibraryTitle{Kind: "series", FolderName: "19-2", TMDB: "61546",
			TMDBName: "19-2", TMDBYear: 2014,
			Details:  store.TitleDetails{Seasons: []store.SeasonInfo{{Number: 1, EpisodeCount: 10}, {Number: 2, EpisodeCount: 10}}},
			Episodes: map[int]int{0: 2, 1: 10, 2: 10, 3: 10, 4: 10}}, []string{"seasons"}},
	}
	for _, c := range cases {
		if got := codes(Reasons(c.t)); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v (%v), want %v", c.about, got, Reasons(c.t), c.want)
		}
	}
	// Its own TMDB ID in the folder: right, whatever else.
	right := film("Whatever (1950) [tmdbid-1]", "Heat", 1995, store.TitleDetails{})
	if r := Reasons(right); r != nil {
		t.Errorf("tagged right: %v", r)
	}
}
