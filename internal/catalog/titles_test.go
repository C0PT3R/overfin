package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// fakeTitles is TMDB for the keeper: a series with two seasons (the
// library has only the first), and a film it doesn't know.
type fakeTitles struct{ seasons []int }

func (f *fakeTitles) Title(_ context.Context, kind string, id int) (*tmdb.Full, error) {
	if kind != "series" || id != 1399 {
		return nil, tmdb.ErrNotFound
	}
	return &tmdb.Full{
		Details: tmdb.Details{
			Title:   tmdb.Title{TMDBID: 1399, Kind: "series", Name: "Le Trône de fer", Year: 2011, Overview: "Neuf familles…", Poster: "/poster1234.jpg"},
			Genres:  []string{"Drame"},
			Cast:    []tmdb.Person{{Name: "Emilia Clarke", Character: "Daenerys", Profile: "/emilia1234.jpg"}},
			Seasons: []tmdb.Season{{Number: 1, Name: "Saison 1", EpisodeCount: 2}, {Number: 2, Name: "Saison 2", EpisodeCount: 1}},
		},
		Runtime: 60,
		Artwork: tmdb.Artwork{Poster: "/poster1234.jpg", Logo: "/logo12345.png"},
	}, nil
}

func (f *fakeTitles) Season(_ context.Context, series, n int) (*tmdb.SeasonFull, error) {
	f.seasons = append(f.seasons, n)
	eps := []tmdb.Episode{{Number: 1, Name: "L'hiver vient", Still: "/still11234.jpg"}, {Number: 2, Name: "La route royale"}}
	if n == 2 {
		eps = eps[:1]
	}
	return &tmdb.SeasonFull{Number: n, Name: "Saison", Poster: "/season1234.jpg", Episodes: eps}, nil
}

// The keeper keeps a library series' page and every season TMDB lists,
// those the library doesn't have too, with their episodes and images.
func TestTitlesKeepEverythingTMDBSays(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	one := 1
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399"},
		{ID: id(2), Kind: "season", Name: "S1", SortName: "s1", SeriesID: id(1), SeasonNumber: &one},
		{ID: id(3), Kind: "movie", Name: "Unknown", SortName: "u", TMDB: "5"},
	}, 1)
	src := &fakeTitles{}
	k := NewTitles(func() TitleSource { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 0)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.seasons) != 2 {
		t.Errorf("seasons asked: %v", src.seasons)
	}
	title, err := st.TitleByTMDB(ctx, "series", "1399")
	if err != nil || title == nil || title.Name != "Le Trône de fer" || title.ItemID != id(1) {
		t.Fatalf("title: %+v %v", title, err)
	}
	var det store.TitleDetails
	if err := json.Unmarshal(title.Details, &det); err != nil || det.Runtime != 60 || len(det.Seasons) != 2 ||
		len(det.Cast) != 1 || det.Cast[0].Profile != "/emilia1234.jpg" {
		t.Errorf("details: %+v %v", det, err)
	}
	two, err := st.SeasonByTMDB(ctx, "1399", 2)
	if err != nil || two == nil || len(two.Episodes) != 1 || two.Episodes[0].Name != "L'hiver vient" {
		t.Errorf("season 2 (not in the library): %+v %v", two, err)
	}
	art, _ := st.Artwork(ctx, []string{"series/tmdb/1399", "series/tmdb/1399/s1/e1"})
	if art["series/tmdb/1399"]["logo"] != "/logo12345.png" || art["series/tmdb/1399/s1/e1"]["still"] != "/still11234.jpg" {
		t.Errorf("artwork: %v", art)
	}
	// Fetched: not asked again on the next pass.
	src.seasons = nil
	if err := k.Once(ctx); err != nil || len(src.seasons) != 0 {
		t.Errorf("second pass: %v %v", src.seasons, err)
	}
}
