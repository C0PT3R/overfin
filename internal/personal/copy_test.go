package personal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

func TestFromServer(t *testing.T) {
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('0'+n)) }
	one := func(v int) *int { return &v }
	played := []any{
		map[string]any{"Id": id(1), "Type": "Movie", "ProviderIds": map[string]string{"Tmdb": "949"}, "UserData": map[string]any{"Played": true, "PlayCount": 3}},
		map[string]any{"Id": id(2), "Type": "Movie", "ProviderIds": map[string]string{"Imdb": "tt0137523"}, "UserData": map[string]any{"Played": true}},
		map[string]any{"Id": id(3), "Type": "Movie", "ProviderIds": map[string]string{"Tmdb": "x/../1"}, "UserData": map[string]any{"Played": true}},
		map[string]any{"Id": id(4), "Type": "Episode", "SeriesId": id(9), "ParentIndexNumber": one(1), "IndexNumber": one(2), "UserData": map[string]any{"Played": true}},
		map[string]any{"Id": id(5), "Type": "Episode", "SeriesId": id(9), "UserData": map[string]any{"Played": true}},
	}
	resumable := []any{
		map[string]any{"Id": id(6), "Type": "Episode", "SeriesId": id(9), "ParentIndexNumber": one(1), "IndexNumber": one(3),
			"UserData": map[string]any{"PlaybackPositionTicks": 6_000_000_000, "LastPlayedDate": "2026-10-20T20:00:00.0000000Z"}},
	}
	favorites := []any{
		map[string]any{"Id": id(9), "Type": "Series", "ProviderIds": map[string]string{"Tvdb": "81189"}, "UserData": map[string]any{"IsFavorite": true}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/Items" || !strings.Contains(r.Header.Get("Authorization"), `Token="their-token"`) || q.Get("userId") != "u" {
			http.NotFound(w, r)
			return
		}
		var items []any
		switch {
		case q.Get("ids") != "":
			items = favorites // the show, asked for by its ID
		case q.Get("isPlayed") == "true":
			items = played
		case q.Get("filters") != "" && strings.Contains(q.Get("filters"), "IsResumable"):
			items = resumable
		case q.Get("isFavorite") == "true":
			items = favorites
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	}))
	defer srv.Close()
	jf, _ := jellyfin.New(srv.URL, "test")

	states, skipped, err := FromServer(context.Background(), jf, jellyfin.Auth{Token: "their-token", DeviceID: "d"}, "u")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.Personal{}
	for _, s := range states {
		got[s.Title] = s
	}
	want := map[string]func(store.Personal) bool{
		"movie/tmdb/949":          func(s store.Personal) bool { return s.Played && s.PlayCount == 3 && s.ItemID == "" },
		"movie/imdb/tt0137523":    func(s store.Personal) bool { return s.Played },
		"series/tvdb/81189/s1/e2": func(s store.Personal) bool { return s.Played },
		"series/tvdb/81189/s1/e3": func(s store.Personal) bool { return s.PositionMs == 600_000 && !s.LastPlayedAt.IsZero() },
		"series/tvdb/81189":       func(s store.Personal) bool { return s.Favorite },
	}
	for key, ok := range want {
		if s, found := got[key]; !found || !ok(s) {
			t.Errorf("%s: %+v (found %v)", key, s, found)
		}
	}
	// A malformed ID and an episode without numbers are left out.
	if len(got) != len(want) || skipped != 2 {
		t.Errorf("got %d titles, skipped %d: %v", len(got), skipped, got)
	}
}
