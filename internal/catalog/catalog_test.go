package catalog

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/jellyfin"
	"github.com/C0PT3R/overfin-gateway/internal/seal"
	"github.com/C0PT3R/overfin-gateway/internal/store"
)

func id(n int) string { return fmt.Sprintf("%032x", n) }

// library is a fake Jellyfin library: its items, when each was last saved,
// and the one token it accepts.
type library struct {
	mu     sync.Mutex
	items  []map[string]any
	saved  map[string]time.Time
	token  string
	hidden map[string]bool // left out of listings, but still there
}

func (l *library) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.URL.Path != "/Items" {
		http.NotFound(w, r)
		return
	}
	if !strings.Contains(r.Header.Get("Authorization"), `Token="`+l.token+`"`) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	var out []map[string]any
	if ids := q.Get("ids"); ids != "" {
		for _, it := range l.items {
			if slices.Contains(strings.Split(ids, ","), it["Id"].(string)) {
				out = append(out, it)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": out, "TotalRecordCount": len(out)})
		return
	}
	since, _ := time.Parse(time.RFC3339, q.Get("minDateLastSaved"))
	for _, it := range l.items {
		if l.hidden[it["Id"].(string)] || l.saved[it["Id"].(string)].Before(since) {
			continue
		}
		out = append(out, it)
	}
	start, _ := strconv.Atoi(q.Get("startIndex"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	total := len(out)
	out = out[min(start, total):min(start+limit, total)]
	json.NewEncoder(w).Encode(map[string]any{"Items": out, "TotalRecordCount": total})
}

func (l *library) add(n int, it map[string]any, at time.Time) {
	it["Id"] = id(n)
	l.items = append(l.items, it)
	l.saved[id(n)] = at
}

type service struct {
	lib     *library
	refused int
}

func (s *service) Auth(context.Context) (jellyfin.Auth, string, error) {
	return jellyfin.Auth{Token: "token-" + strconv.Itoa(s.refused), DeviceID: "d"}, id(999), nil
}

func (s *service) Refused(context.Context) error {
	s.refused++
	return nil
}

func setup(t *testing.T) (*Sync, *library, *store.Store, *time.Time) {
	t.Helper()
	lib := &library{saved: map[string]time.Time{}, token: "token-0", hidden: map[string]bool{}}
	srv := httptest.NewServer(lib)
	t.Cleanup(srv.Close)
	jf, _ := jellyfin.New(srv.URL, "test")
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s := &Sync{Jellyfin: jf, Store: st, Service: &service{lib: lib}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChangesEvery: 5 * time.Minute, FullEvery: 6 * time.Hour, now: func() time.Time { return now }}
	return s, lib, st, &now
}

func counts(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	c, err := st.CatalogCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSync(t *testing.T) {
	s, lib, st, now := setup(t)
	ctx := context.Background()
	early := now.Add(-time.Hour)
	lib.add(1, map[string]any{"Type": "Movie", "Name": "Heat <b>1995</b>", "ProductionYear": 1995,
		"ProviderIds":    map[string]string{"Tmdb": "949", "Imdb": "tt0113277", "Tvdb": "not-an-id"},
		"OfficialRating": "R", "ImageTags": map[string]string{"Primary": "abc123"}}, early)
	lib.add(2, map[string]any{"Type": "Series", "Name": "Dark"}, early)
	lib.add(3, map[string]any{"Type": "Season", "Name": "Season 1", "SeriesId": id(2), "IndexNumber": 1}, early)
	lib.add(4, map[string]any{"Type": "Episode", "Name": "Secrets", "SeriesId": id(2), "SeasonId": id(3),
		"ParentIndexNumber": 1, "IndexNumber": 1}, early)
	lib.add(5, map[string]any{"Type": "MusicAlbum", "Name": "not indexed yet"}, early)
	// An ID that isn't one is left out.
	lib.items = append(lib.items, map[string]any{"Type": "Movie", "Id": "../bad", "Name": "x"})
	lib.saved["../bad"] = early

	// The first run reads everything.
	s.once(ctx)
	if c := counts(t, st); c["movie"] != 1 || c["series"] != 1 || c["season"] != 1 || c["episode"] != 1 {
		t.Fatalf("first pass: %v (%s)", c, s.lastErr)
	}

	// A change: read with the next run, without a full pass.
	*now = now.Add(10 * time.Minute)
	lib.add(7, map[string]any{"Type": "Movie", "Name": "Arrival"}, *now)
	s.once(ctx)
	if c := counts(t, st); c["movie"] != 2 {
		t.Errorf("after a change: %v", c)
	}

	// A title gone, and one missed by the listing but still there: the full
	// pass removes the first only.
	lib.items = slices.DeleteFunc(lib.items, func(it map[string]any) bool { return it["Id"] == id(7) })
	lib.hidden[id(1)] = true
	*now = now.Add(7 * time.Hour)
	s.once(ctx)
	if c := counts(t, st); c["movie"] != 1 {
		t.Errorf("after the full pass: %v (%s)", c, s.lastErr)
	}
	stat, _ := s.Status(ctx)
	if stat.Titles["movie"] != 1 || !stat.FullAt.Equal(*now) || stat.Error != "" {
		t.Errorf("status: %+v", stat)
	}

	// Jellyfin refuses the token: the service gets a new one, which
	// Jellyfin accepts, and the read goes on.
	lib.token = "token-1"
	svc := s.Service.(*service)
	if err := s.Changes(ctx); err != nil || svc.refused != 1 {
		t.Errorf("refused token: %v, renewed %d times", err, svc.refused)
	}
}

func TestConvert(t *testing.T) {
	it, ok := convert(jellyfin.RawItem{Id: id(1), Type: "Movie", Name: "<script>x</script>Heat",
		ProviderIds: map[string]string{"Tmdb": "949; DROP", "Imdb": "tt0113277"},
		ImageTags:   map[string]string{"Primary": "../../etc"}, BackdropImageTags: []string{"ff00"}})
	if !ok || strings.Contains(it.Name, "<") || it.TMDB != "" || it.IMDb != "tt0113277" ||
		it.PrimaryTag != "" || it.BackdropTag != "ff00" || it.SortName != it.Name {
		t.Errorf("converted: %+v", it)
	}
	if _, ok := convert(jellyfin.RawItem{Id: id(1), Type: "Folder"}); ok {
		t.Error("a folder was indexed")
	}
}
