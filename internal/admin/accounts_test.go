package admin

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C0PT3R/overfin/internal/connector"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/seal"
	"github.com/C0PT3R/overfin/internal/store"
)

// signedInPanel is a panel with a database and a fake Jellyfin, and a
// browser already signed in to it.
func signedInPanel(t *testing.T) (*Panel, *browser, *store.Store, *[]string) {
	t.Helper()
	p, idp, _ := newPanel(t)
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var mu sync.Mutex
	var calls []string
	jfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(jfSrv.Close)
	jf, _ := jellyfin.New(jfSrv.URL, "test")
	p.Store = db
	p.Provisioner = &provision.Provisioner{Store: db, Jellyfin: jf, AdminKey: "admin", LogKey: []byte("k"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	if rec := signIn(t, b, idp, []string{"overfin-admins"}); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign in: %d", rec.Code)
	}
	return p, b, db, &calls
}

func TestAccounts(t *testing.T) {
	ctx := context.Background()
	_, b, db, calls := signedInPanel(t)
	acct, owner, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main", JellyfinUserID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", JellyfinUsername: "of-1", JellyfinToken: "t"})
	_ = db.SetPIN(ctx, acct.ID, owner.ID, []byte("hash"))
	_ = db.RecordPIN(ctx, owner.ID, 5, time.Now(), time.Now().Add(time.Hour))
	sess, _, _ := db.CreateSession(ctx, acct.ID, owner.ID, "Firefox on Linux", time.Hour)

	rec := b.do("GET", "/admin/api/accounts", "", nil)
	body := rec.Body.String()
	for _, want := range []string{`"subject":"person-1"`, `"status":"active"`, `"pinLocked":true`, `"device":"Firefox on Linux"`, `"linked":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"t"`) || strings.Contains(body, "jellyfinToken") {
		t.Errorf("a token in the list: %s", body)
	}

	change := `{"label":"Bob","maxStreams":3,"expiresAt":"2020-01-01T00:00:00Z"}`
	rec = b.do("PATCH", "/admin/api/accounts/"+acct.ID, change, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"label":"Bob"`) || !strings.Contains(rec.Body.String(), `"status":"expired"`) ||
		!strings.Contains(rec.Body.String(), `"maxStreams":3`) {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"label":"<b>x</b>"}`, `{"maxStreams":-1}`, `{"expiresAt":"tomorrow"}`, `{"owner":true}`} {
		if rec := b.do("PATCH", "/admin/api/accounts/"+acct.ID, bad, nil); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	b.do("PATCH", "/admin/api/accounts/"+acct.ID, `{"expiresAt":"","disabled":true}`, nil)
	if a, _ := db.AccountByID(ctx, acct.ID); !a.Disabled || !a.Expires.IsZero() {
		t.Errorf("after disabling and clearing the expiry: %+v", a)
	}

	if rec := b.do("POST", "/admin/api/profiles/"+owner.ID+"/unlock-pin", "{}", nil); rec.Code != 204 {
		t.Errorf("unlock: %d", rec.Code)
	}
	if locks, _ := db.PINLocks(ctx, acct.ID); locks[owner.ID] {
		t.Error("still locked")
	}
	if rec := b.do("DELETE", "/admin/api/accounts/"+acct.ID+"/sessions/"+sess.ID, "", nil); rec.Code != 204 {
		t.Errorf("end session: %d", rec.Code)
	}

	rec = b.do("DELETE", "/admin/api/accounts/"+acct.ID, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"history":"deleted"`) {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := db.AccountByID(ctx, acct.ID); err == nil {
		t.Error("account still there")
	}
	if !strings.Contains(strings.Join(*calls, " "), "DELETE /Users/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("Jellyfin user not deleted: %v", *calls)
	}
}

// Without signing in, none of it.
func TestAccountsNeedSignIn(t *testing.T) {
	p, _, _, _ := signedInPanel(t)
	stranger := &browser{h: p.Handler(), cookies: map[string]string{}}
	for _, r := range [][2]string{{"GET", "/admin/api/accounts"}, {"PATCH", "/admin/api/accounts/acct_x"}, {"DELETE", "/admin/api/accounts/acct_x"}} {
		body := ""
		if r[0] == "PATCH" {
			body = "{}"
		}
		if rec := stranger.do(r[0], r[1], body, nil); rec.Code != 401 {
			t.Errorf("%s %s: %d", r[0], r[1], rec.Code)
		}
	}
}

type fakeDirectory struct {
	people map[string]connector.Person
	err    error
	calls  int
}

func (f *fakeDirectory) People(context.Context) (map[string]connector.Person, error) {
	f.calls++
	return f.people, f.err
}

func TestAccountsWithNames(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main", JellyfinUserID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", JellyfinUsername: "of-1", JellyfinToken: "t"})
	dir := &fakeDirectory{people: map[string]connector.Person{"person-1": {Username: "alice", Name: "Alice", Active: true}}}
	p.Directory = dir

	body := b.do("GET", "/admin/api/accounts", "", nil).Body.String()
	if !strings.Contains(body, `"username":"alice"`) || !strings.Contains(body, `"directory":"ok"`) {
		t.Errorf("with a connector: %s", body)
	}
	// Kept briefly in memory: a second look doesn't ask again.
	b.do("GET", "/admin/api/accounts", "", nil)
	if dir.calls != 1 {
		t.Errorf("asked the directory %d times", dir.calls)
	}

	// A failing connector doesn't break the list.
	p.Directory = &fakeDirectory{err: io.ErrUnexpectedEOF}
	p.people = nil
	rec := b.do("GET", "/admin/api/accounts", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"directory":"unreachable"`) || strings.Contains(rec.Body.String(), "alice") {
		t.Errorf("failing connector: %d %s", rec.Code, rec.Body)
	}
}
