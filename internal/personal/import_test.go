package personal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

func id(n int) string { return fmt.Sprintf("%032x", n) }

func TestImport(t *testing.T) {
	var calls atomic.Int32
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		// Only the profile's own token, for its own user.
		if !strings.Contains(r.Header.Get("Authorization"), `Token="profile-token"`) || q.Get("userId") != id(100) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var items []any
		switch {
		case q.Get("isPlayed") == "true":
			items = []any{map[string]any{"Id": id(1), "UserData": map[string]any{"Played": true, "PlayCount": 3,
				"LastPlayedDate": "2026-10-01T20:00:00.0000000Z"}}}
		case q.Get("filters") == "IsResumable":
			items = []any{map[string]any{"Id": id(4), "UserData": map[string]any{"PlaybackPositionTicks": 12_000_000_000}}}
		case q.Get("isFavorite") == "true":
			items = []any{map[string]any{"Id": id(2), "UserData": map[string]any{"IsFavorite": true}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
	}))
	defer jf.Close()
	client, _ := jellyfin.New(jf.URL, "test")
	box, _ := seal.New(bytes.Repeat([]byte{4}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, p, err := st.CreateAccount(ctx, "subject", store.NewOwner{Name: "Main", JellyfinUserID: id(100),
		JellyfinUsername: "of-test", JellyfinToken: "profile-token"})
	if err != nil {
		t.Fatal(err)
	}
	one, two := 1, 2
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"},
		{ID: id(2), Kind: "movie", Name: "Heat (4K)", SortName: "Heat", TMDB: "949"},
		{ID: id(3), Kind: "series", Name: "Dark", SortName: "Dark", TMDB: "70523"},
		{ID: id(4), Kind: "episode", Name: "Secrets", SortName: "Secrets", SeriesID: id(3), SeasonNumber: &one, EpisodeNumber: &two},
	}, 1)
	st.RefreshTitleKeys(ctx)
	im := &Importer{Jellyfin: client, Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Every: time.Hour}

	// Before the index has read the library, nothing happens.
	if err := im.Once(ctx); err != nil || calls.Load() != 0 {
		t.Fatalf("before the index: %v, %d calls", err, calls.Load())
	}
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	// Indexed, but without keys yet: still nothing.
	st.PutCatalog(ctx, []store.CatalogItem{{ID: id(5), Kind: "movie", Name: "New", SortName: "New"}}, 1)
	if err := im.Once(ctx); err != nil || calls.Load() != 0 {
		t.Fatalf("before the keys: %v, %d calls", err, calls.Load())
	}
	st.RefreshTitleKeys(ctx)
	if err := im.Once(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.PersonalStates(ctx, p.ID, []string{"movie/tmdb/949", "series/tmdb/70523/s1/e2"})
	heat, ep := got["movie/tmdb/949"], got["series/tmdb/70523/s1/e2"]
	// The two copies of Heat are one title: watched three times, a favorite.
	if !heat.Played || heat.PlayCount != 3 || !heat.Favorite || heat.LastPlayedAt.IsZero() {
		t.Errorf("Heat: %+v", heat)
	}
	if ep.PositionMs != 1_200_000 || ep.Played {
		t.Errorf("the episode: %+v", ep)
	}
	// Once only.
	before := calls.Load()
	im.Once(ctx)
	if calls.Load() != before {
		t.Errorf("imported again: %d more calls", calls.Load()-before)
	}
}
