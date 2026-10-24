package images

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Library artwork kept on disk: fetched once at the kept sizes, served
// from disk after, fetched again when old, deleted once unwanted.
func TestKeep(t *testing.T) {
	jpg := sampleJPEG(t)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Path == "/w185/missing1234.jpg" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(jpg)
	}))
	defer srv.Close()
	old := tmdbImageBase
	tmdbImageBase = srv.URL + "/"
	defer func() { tmdbImageBase = old }()

	dir := t.TempDir()
	p := New(nil, 1<<20)
	p.ArtworkDir = dir
	ctx := context.Background()
	poster := TMDBRef{Type: "poster", Path: "/poster1234.jpg"}
	still := TMDBRef{Type: "still", Path: "/still12345.jpg"}
	wanted := []TMDBRef{poster, still, poster, {Type: "still", Path: "/missing1234.jpg"}, {Type: "poster", Path: "../../etc"}}

	fetched, deleted, err := p.Keep(ctx, wanted, time.Now().Add(-time.Hour))
	if err != nil || fetched != 3 || deleted != 0 {
		t.Fatalf("first pass: %d %d %v", fetched, deleted, err)
	}
	for _, f := range []string{"poster/w185/poster1234.jpg", "poster/w342/poster1234.jpg", "still/w185/still12345.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("not kept: %s", f)
		}
	}

	// Served from disk: TMDB isn't asked. The check still runs.
	n := asked.Load()
	if data, ct, err := p.TMDB(ctx, poster, "medium", func(context.Context) error { return nil }); err != nil || ct != "image/jpeg" || len(data) == 0 {
		t.Fatalf("served: %v", err)
	}
	if asked.Load() != n {
		t.Error("asked TMDB for a kept image")
	}
	if _, _, err := p.TMDB(ctx, still, "small", func(context.Context) error { return ErrHidden }); err != ErrHidden {
		t.Errorf("check skipped: %v", err)
	}

	// Fresh: nothing fetched again. Old: fetched again.
	if fetched, _, _ := p.Keep(ctx, wanted, time.Now().Add(-time.Hour)); fetched != 0 {
		t.Errorf("fetched again while fresh: %d", fetched)
	}
	if fetched, _, _ := p.Keep(ctx, wanted, time.Now().Add(time.Hour)); fetched != 3 {
		t.Errorf("old ones: %d", fetched)
	}

	// The still's title left the libraries: deleted; nothing wanted at all
	// deletes nothing.
	os.WriteFile(filepath.Join(dir, "poster", "w185", ".new-123"), []byte("half"), 0o600)
	if _, deleted, err := p.Keep(ctx, []TMDBRef{poster}, time.Now().Add(-time.Hour)); err != nil || deleted != 1 {
		t.Errorf("unwanted: %d %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "still/w185/still12345.jpg")); err == nil {
		t.Error("unwanted still kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "poster", "w185", ".new-123")); err == nil {
		t.Error("half-written file kept")
	}
	if _, deleted, _ := p.Keep(ctx, nil, time.Now().Add(-time.Hour)); deleted != 0 {
		t.Errorf("empty wanted deleted %d", deleted)
	}

	// No more TMDB: every kept image goes, and the folder stays.
	if err := p.ForgetKept(); err != nil {
		t.Fatal(err)
	}
	if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
		t.Errorf("after forgetting: %v %v", left, err)
	}
	if err := (&Pipeline{ArtworkDir: filepath.Join(dir, "none")}).ForgetKept(); err != nil {
		t.Errorf("no folder: %v", err)
	}
}
