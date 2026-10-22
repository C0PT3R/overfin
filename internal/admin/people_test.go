package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

type links []string

func (l *links) SigninLink(_ context.Context, email string) (string, error) {
	*l = append(*l, email)
	return "/api/v1/signin#link=x", nil
}

func TestPeople(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	me, _ := db.CreateUser(context.Background(), "me@example.org", true)
	made := &links{}
	p := &Panel{Features: &memFeatures{f: config.DefaultFeatures()}, Store: db, People: made,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		SiteAdmin: func(*http.Request) (string, bool, bool) { return "me@example.org", true, true }}
	h := p.SiteHandler(false)
	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest("GET", "/admin/login", nil))
	cookie := login.Result().Cookies()[0]
	do := func(method, path string, body any) (int, map[string]any) {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, out := do("POST", "/admin/api/people", map[string]any{"email": "Bob <bob@example.org>"}); code != 400 {
		t.Errorf("not a plain email: %d %v", code, out)
	}
	code, bob := do("POST", "/admin/api/people", map[string]any{"email": "bob@example.org"})
	if code != 200 || bob["admin"] != false {
		t.Fatalf("adding Bob: %d %v", code, bob)
	}
	if code, _ := do("POST", "/admin/api/people", map[string]any{"email": "BOB@example.org"}); code != 409 {
		t.Errorf("the same email twice: %d", code)
	}
	id := bob["id"].(string)
	if code, out := do("PATCH", "/admin/api/people/"+id, map[string]any{"admin": true}); code != 200 || out["admin"] != true {
		t.Errorf("making Bob an admin: %d %v", code, out)
	}
	if code, out := do("POST", "/admin/api/people/"+id+"/link", map[string]any{}); code != 200 ||
		!strings.HasPrefix(out["link"].(string), "/api/v1/signin#link=") || len(*made) != 1 || (*made)[0] != "bob@example.org" {
		t.Errorf("Bob's link: %d %v %v", code, out, *made)
	}
	// Admins keep themselves: no stepping down, no removing themselves.
	if code, _ := do("PATCH", "/admin/api/people/"+me.Subject, map[string]any{"admin": false}); code != 400 {
		t.Errorf("stepping down: %d", code)
	}
	if code, _ := do("DELETE", "/admin/api/people/"+me.Subject, nil); code != 400 {
		t.Errorf("removing myself: %d", code)
	}
	code, list := do("GET", "/admin/api/people", nil)
	if items := list["items"].([]any); code != 200 || len(items) != 2 || items[1].(map[string]any)["you"] != true {
		t.Errorf("people: %d %v", code, list)
	}
	if code, _ := do("DELETE", "/admin/api/people/"+id, nil); code != 204 {
		t.Errorf("removing Bob: %d", code)
	}
	if _, err := db.UserByEmail(context.Background(), "bob@example.org"); err != store.ErrNotFound {
		t.Errorf("Bob is still there: %v", err)
	}
}
