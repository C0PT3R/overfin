package ratings

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// fakeSource rates every film G, except those it fails on or doesn't know.
type fakeSource struct {
	fail    map[int]bool
	unknown map[int]bool
	asked   map[int]int
}

func (f *fakeSource) Scales(ctx context.Context, kind string) (map[string][]store.RatingLevel, error) {
	return map[string][]store.RatingLevel{"US": {{Certification: "G", Position: 1}}}, nil
}

func (f *fakeSource) Ratings(ctx context.Context, kind string, id int) (map[string][]string, error) {
	f.asked[id]++
	switch {
	case f.fail[id]:
		return nil, errors.New("seerr: answered 500")
	case f.unknown[id]:
		return nil, ErrUnknown
	}
	return map[string][]string{"US": {"G"}}, nil
}

func TestOneFailingTitleDoesntStopThePass(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var items []store.CatalogItem
	for _, tmdb := range []string{"1", "2", "3", "4"} {
		items = append(items, store.CatalogItem{ID: "0000000000000000000000000000000" + tmdb, Kind: "movie",
			Name: tmdb, SortName: tmdb, TMDB: tmdb})
	}
	if err := st.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{fail: map[int]bool{2: true}, unknown: map[int]bool{4: true}, asked: map[int]int{}}
	k := NewKeeper(func() Source { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), 0, 0)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if fetched, total, _ := st.RatingProgress(ctx); fetched != 3 || total != 4 {
		t.Errorf("after the first pass: %d of %d", fetched, total)
	}
	if src.asked[2] != 1 {
		t.Errorf("the failing title was asked for %d times in one pass", src.asked[2])
	}
	// The next pass tries it again, and only it.
	delete(src.fail, 2)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if fetched, _, _ := st.RatingProgress(ctx); fetched != 4 || src.asked[1] != 1 || src.asked[2] != 2 {
		t.Errorf("after the second pass: %d fetched, asked %v", fetched, src.asked)
	}
	c := store.Ceiling{Country: "US", Movie: "G", Series: "G"}
	if ok, _ := st.TitleAllowed(ctx, c, "movie", "2"); !ok {
		t.Error("film 2 not allowed once rated")
	}
	if ok, _ := st.TitleAllowed(ctx, c, "movie", "4"); ok {
		t.Error("film 4, unknown to TMDB, allowed")
	}
}
