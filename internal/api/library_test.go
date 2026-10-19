package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C0PT3R/overfin/internal/api"
	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/playback"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/seal"
	"github.com/C0PT3R/overfin/internal/server"
	"github.com/C0PT3R/overfin/internal/store"
)

const (
	userJF  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	token   = "profile-token"
	subject = "test-subject"
)

// fakeJellyfin serves 7 movies and checks every call uses the profile's token.
type fakeJellyfin struct {
	mu       sync.Mutex
	badAuth  int
	lastQury url.Values
}

func movie(i int) map[string]any {
	return map[string]any{"Id": fmt.Sprintf("%032x", i+1), "Name": fmt.Sprintf("Movie %d", i), "Type": "Movie"}
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(r.Header.Get("Authorization"), `Token="`+token+`"`) {
		f.badAuth++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	f.lastQury = q
	if q.Get("userId") != userJF && !strings.HasPrefix(r.URL.Path, "/MediaSegments/") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/Items" && q.Has("ids"):
		var items []map[string]any
		for _, id := range strings.Split(q.Get("ids"), ",") {
			n, _ := strconv.ParseInt(id, 16, 64)
			items = append(items, movie(int(n)-1))
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	case r.URL.Path == "/Items" && q.Get("enableImages") == "false":
		var items []map[string]any
		for i := 0; i < 7; i++ {
			items = append(items, map[string]any{"Id": fmt.Sprintf("%032x", i+1)})
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	case r.URL.Path == "/Items":
		start, _ := strconv.Atoi(q.Get("startIndex"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		var items []map[string]any
		for i := start; i < 7 && i < start+limit; i++ {
			items = append(items, movie(i))
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 7})
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 1):
		json.NewEncoder(w).Encode(movie(0))
	case r.Method == "POST" && r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 1)+"/PlaybackInfo":
		json.NewEncoder(w).Encode(map[string]any{
			"PlaySessionId": "abc123",
			"MediaSources": []map[string]any{{
				"Id": fmt.Sprintf("%032x", 1), "Name": "1080p", "Protocol": "File", "Path": "/mnt/media/secret.mkv",
				"SupportsTranscoding": true, "TranscodingSubProtocol": "hls",
				"TranscodingUrl": "/videos/" + fmt.Sprintf("%032x", 1) + "/master.m3u8?ApiKey=" + token + "&MediaSourceId=x",
				"RunTimeTicks":   72000000000,
				"MediaStreams": []map[string]any{
					{"Index": 1, "Type": "Audio", "Codec": "eac3", "Language": "fre", "DisplayTitle": "Français - EAC3 5.1", "Channels": 6, "IsDefault": true},
					{"Index": 3, "Type": "Subtitle", "Codec": "srt", "Language": "eng", "DisplayTitle": "English", "IsTextSubtitleStream": true},
					{"Index": 4, "Type": "Subtitle", "Codec": "PGSSUB", "DisplayTitle": "Blu-ray"},
				},
			}},
		})
	case r.URL.Path == "/MediaSegments/"+fmt.Sprintf("%032x", 1):
		json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
			{"Type": "Intro", "StartTicks": 50000000, "EndTicks": 900000000},
		}})
	case strings.HasPrefix(r.URL.Path, "/Items/"):
		w.WriteHeader(http.StatusNotFound) // not visible to this user
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newServer(t *testing.T) (*httptest.Server, *fakeJellyfin) {
	t.Helper()
	fake := &fakeJellyfin{}
	jfSrv := httptest.NewServer(fake)
	t.Cleanup(jfSrv.Close)
	jf, _ := jellyfin.New(jfSrv.URL, "test")

	box, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, _, err := st.CreateAccount(context.Background(), subject, store.NewOwner{
		Name: "Main", JellyfinUserID: userJF, JellyfinUsername: "of-test", JellyfinToken: token,
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	prov := &provision.Provisioner{Store: st, Jellyfin: jf, LogKey: []byte("k"), Logger: logger}
	ids := identity.NewForwardAuth([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, "Remote-User", "")
	lib := api.Library{Provisioner: prov, Jellyfin: jf, CursorKey: []byte("cursor-key"), Logger: logger}
	h, err := server.New(logger, ids, lib,
		api.Playback{Library: lib, Tickets: playback.NewTickets(time.Hour), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, fake
}

func get(t *testing.T, srv *httptest.Server, path string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Remote-User", subject)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

type page struct {
	Items      []struct{ ID, Kind, Name string }
	NextCursor string
	TotalCount int
	Seed       string
	Type       string // set on errors
}

func TestPagingWithSignedCursors(t *testing.T) {
	srv, fake := newServer(t)

	var p1 page
	if code := get(t, srv, "/api/v1/items?kind=movie&limit=3", &p1); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(p1.Items) != 3 || p1.TotalCount != 7 || p1.NextCursor == "" || p1.Items[0].Kind != "movie" {
		t.Fatalf("first page: %+v", p1)
	}
	if fake.lastQury.Get("includeItemTypes") != "Movie" || fake.lastQury.Get("recursive") != "true" {
		t.Errorf("Jellyfin query: %v", fake.lastQury)
	}

	var p2 page
	get(t, srv, "/api/v1/items?kind=movie&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &p2)
	if len(p2.Items) != 3 || p2.Items[0].Name != "Movie 3" {
		t.Errorf("second page: %+v", p2)
	}

	// The same cursor with other filters is refused.
	var bad page
	code := get(t, srv, "/api/v1/items?kind=series&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &bad)
	if code != http.StatusBadRequest || bad.Type != "invalid-cursor" {
		t.Errorf("reused cursor: %d %q", code, bad.Type)
	}
	if fake.badAuth != 0 {
		t.Errorf("%d Jellyfin calls without the profile's token", fake.badAuth)
	}
}

func TestInvalidQueries(t *testing.T) {
	srv, _ := newServer(t)
	for _, q := range []string{
		"?kind=folder",
		"?sort=name&seed=abc",   // a seed without random order
		"?sort=random&seed=a-b", // seeds are letters and digits
		"?limit=0",
		"?limit=500",
		"?library=../../x",
		"?genre=a|b",
		"?yearFrom=12",
		"?unknown=1",
		"?parent=item_ZZZ",
	} {
		var p page
		if code := get(t, srv, "/api/v1/items"+q, &p); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, code)
		}
	}
}

func TestItemDetailAndHiddenItems(t *testing.T) {
	srv, _ := newServer(t)
	var it struct{ ID, Name string }
	if code := get(t, srv, "/api/v1/items/item_"+fmt.Sprintf("%032x", 1), &it); code != 200 || it.Name != "Movie 0" {
		t.Errorf("visible item: %d %+v", code, it)
	}
	// Jellyfin says 404 for items the profile may not see; so does Overfin.
	if code := get(t, srv, "/api/v1/items/item_"+fmt.Sprintf("%032x", 99), nil); code != http.StatusNotFound {
		t.Errorf("hidden item: status %d", code)
	}
	if code := get(t, srv, "/api/v1/items/not-an-id", nil); code != http.StatusNotFound {
		t.Errorf("malformed ID: status %d", code)
	}
}

func TestNoAccountMeansSetupRequired(t *testing.T) {
	srv, _ := newServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/libraries", nil)
	req.Header.Set("Remote-User", "someone-new")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var p page
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if p.Type != "account-setup-required" {
		t.Errorf("type = %q", p.Type)
	}
}

func names(p page) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Name)
	}
	return out
}

func TestRandomOrderIsRepeatable(t *testing.T) {
	srv, _ := newServer(t)

	var a, b, other page
	get(t, srv, "/api/v1/items?sort=random&seed=abc&limit=7", &a)
	get(t, srv, "/api/v1/items?sort=random&seed=abc&limit=7", &b)
	get(t, srv, "/api/v1/items?sort=random&seed=xyz&limit=7", &other)
	if strings.Join(names(a), ",") != strings.Join(names(b), ",") {
		t.Errorf("same seed, different order: %v vs %v", names(a), names(b))
	}
	if strings.Join(names(a), ",") == strings.Join(names(other), ",") {
		t.Errorf("different seeds, same order: %v", names(a))
	}
	if len(a.Items) != 7 || a.Seed != "abc" {
		t.Errorf("page: %d items, seed %q", len(a.Items), a.Seed)
	}
}

func TestRandomPagesCoverEverythingOnce(t *testing.T) {
	srv, _ := newServer(t)

	// No seed given: the server picks one, returns it, and the cursor keeps
	// using it on later pages.
	var p page
	get(t, srv, "/api/v1/items?sort=random&limit=3", &p)
	if p.Seed == "" {
		t.Fatal("no seed returned")
	}
	firstSeed := p.Seed
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("too many pages")
		}
		for _, n := range names(p) {
			if seen[n] {
				t.Fatalf("%s appeared twice", n)
			}
			seen[n] = true
		}
		if p.NextCursor == "" {
			break
		}
		next := p.NextCursor
		p = page{}
		get(t, srv, "/api/v1/items?sort=random&limit=3&cursor="+url.QueryEscape(next), &p)
		if p.Seed != firstSeed {
			t.Fatalf("seed changed between pages: %q then %q", firstSeed, p.Seed)
		}
	}
	if len(seen) != 7 {
		t.Errorf("pages covered %d of 7 items", len(seen))
	}
}

func post(t *testing.T, srv *httptest.Server, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestStartPlaybackNeverLeaksUpstream(t *testing.T) {
	srv, _ := newServer(t)
	code, body := post(t, srv, "/api/v1/playback",
		`{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{"containers":["mp4"],"videoCodecs":["h264"],"audioCodecs":["aac"]}}`)
	if code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, leak := range []string{token, "ApiKey", "/videos/", "/mnt/", "abc123", "MediaSourceId"} {
		if strings.Contains(body, leak) {
			t.Errorf("response contains %q: %s", leak, body)
		}
	}
	var s struct {
		ID, Method     string
		Stream         struct{ URL, Protocol string }
		DurationMs     int64
		AudioTracks    []struct{ ID, Label, Language string }
		SubtitleTracks []struct{ ID, URL string }
		Segments       []struct{ Kind string }
	}
	json.Unmarshal([]byte(body), &s)
	if !strings.HasPrefix(s.ID, "play_") || s.Method != "transcode" || s.Stream.Protocol != "hls" ||
		!strings.HasPrefix(s.Stream.URL, "/api/v1/stream/") || !strings.HasSuffix(s.Stream.URL, "/master.m3u8") {
		t.Errorf("session: %+v", s)
	}
	if s.DurationMs != 7_200_000 || len(s.AudioTracks) != 1 || s.AudioTracks[0].ID != "a1" || s.AudioTracks[0].Language != "fre" {
		t.Errorf("tracks/duration: %+v", s)
	}
	// Text subtitles get a WebVTT URL; image subtitles don't (they're burned in).
	if len(s.SubtitleTracks) != 2 || s.SubtitleTracks[0].URL == "" || s.SubtitleTracks[1].URL != "" {
		t.Errorf("subtitles: %+v", s.SubtitleTracks)
	}
	if len(s.Segments) != 1 || s.Segments[0].Kind != "intro" {
		t.Errorf("segments: %+v", s.Segments)
	}
}

func TestStartPlaybackRefuses(t *testing.T) {
	srv, _ := newServer(t)
	for _, body := range []string{
		`{"itemId":"not-an-id","capabilities":{}}`,
		`{"itemId":"item_` + fmt.Sprintf("%032x", 1) + `","capabilities":{},"audioTrack":"s1"}`,
		`{"itemId":"item_` + fmt.Sprintf("%032x", 1) + `","capabilities":{},"startPositionMs":-5}`,
		`{"itemId":"item_` + fmt.Sprintf("%032x", 1) + `","capabilities":{},"extra":true}`,
	} {
		if code, _ := post(t, srv, "/api/v1/playback", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
	}
	// An item the profile may not see.
	if code, _ := post(t, srv, "/api/v1/playback", `{"itemId":"item_`+fmt.Sprintf("%032x", 99)+`","capabilities":{}}`); code != http.StatusNotFound {
		t.Errorf("hidden item: status %d", code)
	}
}
