package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// An app key, made in the panel, shown once; with it, an app reads totals
// per title, nothing per person; revoked, it's refused.
func TestApps(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	_, prof, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main", JellyfinUserID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", JellyfinUsername: "of-1", JellyfinToken: "t"})
	db.PutCatalog(ctx, []store.CatalogItem{{ID: strings.Repeat("0", 31) + "a", Kind: "movie", Name: "Heat", SortName: "h", TMDB: "949", IMDb: "tt0113277"}}, 1)
	db.RefreshTitleKeys(ctx)
	db.RecordPlay(ctx, prof.ID, "movie/tmdb/949", "", 0, true, time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC))

	rec := b.do("POST", "/admin/api/apps", `{"name":"Stewarr","scope":"watch-stats"}`, nil)
	var made struct{ ID, Key string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 201 || !strings.HasPrefix(made.Key, "ofapp_") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if list := b.do("GET", "/admin/api/apps", "", nil).Body.String(); strings.Contains(list, made.Key) || !strings.Contains(list, "Stewarr") {
		t.Errorf("the key shown again: %s", list)
	}

	app := &browser{h: b.h, cookies: map[string]string{}} // no admin session
	rec = app.do("GET", "/apps/v1/watch-stats", "", map[string]string{"Authorization": "Bearer " + made.Key})
	var stats struct {
		Titles []struct {
			Kind, TmdbId, ImdbId, LastPlayed string
			Plays, Viewers                   int
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &stats)
	if rec.Code != 200 || len(stats.Titles) != 1 || stats.Titles[0].TmdbId != "949" || stats.Titles[0].ImdbId != "tt0113277" ||
		stats.Titles[0].Plays != 1 || stats.Titles[0].Viewers != 1 || stats.Titles[0].LastPlayed != "2026-10-20" {
		t.Errorf("stats: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), prof.ID) || strings.Contains(rec.Body.String(), "person-1") {
		t.Errorf("someone named: %s", rec.Body)
	}
	for _, bad := range []string{"", "Bearer nope", "Bearer ofapp_wrong", made.Key} {
		if rec := app.do("GET", "/apps/v1/watch-stats", "", map[string]string{"Authorization": bad}); rec.Code != 401 {
			t.Errorf("%q: %d", bad, rec.Code)
		}
	}
	if rec := app.do("GET", "/apps/v1/watch-stats?since=yesterday", "", map[string]string{"Authorization": "Bearer " + made.Key}); rec.Code != 400 {
		t.Errorf("bad since: %d", rec.Code)
	}
	if rec := b.do("DELETE", "/admin/api/apps/"+made.ID, "", nil); rec.Code != 204 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := app.do("GET", "/apps/v1/watch-stats", "", map[string]string{"Authorization": "Bearer " + made.Key}); rec.Code != 401 {
		t.Errorf("revoked: %d", rec.Code)
	}
	if rec := b.do("POST", "/admin/api/apps", `{"name":"x","scope":"everything"}`, nil); rec.Code != 400 {
		t.Errorf("unknown scope: %d", rec.Code)
	}
}
