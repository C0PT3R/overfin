package catalog

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// The texts' keeper fetches a title someone asked for in another language
// offered, its seasons and episodes too, and the library's others slowly;
// never the default language's.
func TestTexts(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA")
	st.PutCatalog(ctx, []store.CatalogItem{{ID: id(1), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399"}}, 1)
	st.RefreshTitleKeys(ctx)

	var asked []string
	src := &fakeTitles{}
	x := NewTexts(func(language string) TitleSource { asked = append(asked, language); return src }, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)), 0, time.Hour)

	// The default language is never fetched here: Titles keeps it.
	if err := x.fetch(ctx, want{"fr-CA", "series", "1399"}); err != nil || len(asked) != 0 {
		t.Fatalf("default: %v %v", err, asked)
	}
	// Nor a language not offered.
	if err := x.fetch(ctx, want{"de-DE", "series", "1399"}); err != nil || len(asked) != 0 {
		t.Fatalf("not offered: %v %v", err, asked)
	}
	if err := x.slowly(ctx); err != nil || len(asked) != 1 || asked[0] != "en-CA" {
		t.Fatalf("slowly: %v %v", err, asked)
	}
	if len(src.seasons) != 2 {
		t.Errorf("seasons fetched: %v", src.seasons)
	}
	// With the default language's data kept, as Titles keeps it, the
	// episode reads in en-CA.
	st.SetTitleDetails(ctx, store.Title{Kind: "series", TMDB: "1399", Name: "Le Trône de fer"}, []byte(`{}`), time.Now())
	st.SetSeason(ctx, "1399", store.Season{Number: 1, Name: "Saison 1", Episodes: []store.Episode{{Number: 2, Name: "Défaut"}}}, time.Now())
	infos, err := st.TitleInfos(ctx, []string{"series/tmdb/1399/s1/e2"}, "2026-10-23", "en-CA")
	if err != nil {
		t.Fatal(err)
	}
	// The fake answers in French whatever the language: the texts kept are
	// its; what matters is that they're there.
	if e := infos["series/tmdb/1399/s1/e2"]; e.Untranslated || e.Name != "La route royale" {
		t.Errorf("episode: %+v", e)
	}
	// Fresh now: neither wanted nor slow passes fetch it again.
	x.fetch(ctx, want{"en-CA", "series", "1399"})
	x.slowly(ctx)
	if len(asked) != 1 {
		t.Errorf("fetched again: %v", asked)
	}
}

// A library title TMDB doesn't know doesn't hold up the others' texts: the
// slow pass leaves it for a day and goes on.
func TestTextsGoPastUnknownTitles(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA")
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Gone", SortName: "gone", TMDB: "1"},
		{ID: id(2), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	x := NewTexts(func(string) TitleSource { return &fakeTitles{} }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), 0, time.Hour)
	for range 3 {
		if err := x.slowly(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := st.HasTexts(ctx, "en-CA", "series", "1399", time.Now().Add(-time.Hour)); !ok {
		t.Error("the series' texts never came")
	}
}
