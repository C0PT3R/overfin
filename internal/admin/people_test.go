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
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// sent keeps the invitations sent by email.
type sent []string

func (l *sent) CanMail() bool { return true }

func (l *sent) SendInvitation(_ context.Context, to, link string) error {
	*l = append(*l, to+" "+link)
	return nil
}

func TestPeople(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	me, _ := db.CreateUser(context.Background(), "me@example.org", true)
	made := &sent{}
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

	const pw = "first password 1"
	if code, out := do("POST", "/admin/api/people", map[string]any{"email": "Bob <bob@example.org>", "password": pw}); code != 400 {
		t.Errorf("not a plain email: %d %v", code, out)
	}
	// The password rules: 10 characters, a digit, a special one.
	for bad, want := range map[string]string{"short 1": "10 characters", "no digits here": "digit", "nospecial123": "special"} {
		if code, out := do("POST", "/admin/api/people", map[string]any{"email": "bob@example.org", "password": bad}); code != 400 ||
			!strings.Contains(out["detail"].(string), want) {
			t.Errorf("%q: %d %v", bad, code, out)
		}
	}
	code, bob := do("POST", "/admin/api/people", map[string]any{"email": "bob@example.org", "password": pw, "mustChange": true})
	if code != 200 || bob["admin"] != false || bob["mustChange"] != true {
		t.Fatalf("adding Bob: %d %v", code, bob)
	}
	if u, _ := db.UserByEmail(context.Background(), "bob@example.org"); u.PasswordHash == "" || !u.MustChange || !u.Confirmed {
		t.Errorf("Bob: %+v", u)
	}
	if code, _ := do("POST", "/admin/api/people", map[string]any{"email": "BOB@example.org", "password": pw}); code != 409 {
		t.Errorf("the same email twice: %d", code)
	}
	id := bob["id"].(string)
	if code, out := do("PATCH", "/admin/api/people/"+id, map[string]any{"admin": true}); code != 200 || out["admin"] != true {
		t.Errorf("making Bob an admin: %d %v", code, out)
	}
	// A new password from an admin ends Bob's logins.
	db.CreateLogin(context.Background(), id, nil, "", time.Hour)
	if code, out := do("PUT", "/admin/api/people/"+id+"/password", map[string]any{"password": "second password 2", "mustChange": false}); code != 200 {
		t.Errorf("Bob's new password: %d %v", code, out)
	}
	if u, _ := db.UserByEmail(context.Background(), "bob@example.org"); u.MustChange {
		t.Error("Bob must still change it")
	}
	// Invitations by email, and the rules, on the registration page.
	if code, out := do("POST", "/admin/api/registration/send", map[string]any{"email": "ann@example.org", "link": "https://media.example.org/invite/abc"}); code != 200 ||
		len(*made) != 1 || (*made)[0] != "ann@example.org https://media.example.org/invite/abc" {
		t.Errorf("sending an invitation: %d %v %v", code, out, *made)
	}
	if code, _ := do("POST", "/admin/api/registration/send", map[string]any{"email": "ann@example.org", "link": "https://evil.example/x"}); code != 400 {
		t.Errorf("not an invitation's link: %d", code)
	}
	if code, out := do("PUT", "/admin/api/registration", map[string]any{"rules": map[string]any{"minLength": 9}}); code != 400 {
		t.Errorf("under 10: %d %v", code, out)
	}
	if code, out := do("PUT", "/admin/api/registration", map[string]any{"rules": map[string]any{"minLength": 12, "digit": true}}); code != 200 ||
		out["rules"].(map[string]any)["minLength"] != float64(12) || out["mail"] != true {
		t.Errorf("new rules: %d %v", code, out)
	}
	// Admins keep themselves: no stepping down, no removing themselves.
	if code, _ := do("PATCH", "/admin/api/people/"+me.Subject, map[string]any{"admin": false}); code != 400 {
		t.Errorf("stepping down: %d", code)
	}
	if code, _ := do("DELETE", "/admin/api/people/"+me.Subject, nil); code != 400 {
		t.Errorf("removing myself: %d", code)
	}
	// Nor through their account, in Users: disabled or deleted, they'd lose
	// the panel.
	mine, _, _ := db.CreateAccount(context.Background(), me.Subject, store.NewOwner{Name: "Me"})
	if code, _ := do("PATCH", "/admin/api/accounts/"+mine.ID, map[string]any{"disabled": true}); code != 400 {
		t.Errorf("disabling my account: %d", code)
	}
	if code, _ := do("DELETE", "/admin/api/accounts/"+mine.ID, nil); code != 400 {
		t.Errorf("deleting my account: %d", code)
	}
	if a, _ := db.AccountByID(context.Background(), mine.ID); a == nil || a.Disabled {
		t.Errorf("my account: %+v", a)
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
