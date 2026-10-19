package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/seal"
	"github.com/C0PT3R/overfin/internal/store"
)

const jfUser = "b691e62a7c7244b6934a160398466052"

// fakeJellyfin records what Overfin does, and can fail on demand.
type fakeJellyfin struct {
	mu        sync.Mutex
	created   []string
	deleted   []string
	policy    map[string]any
	failLogin bool
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method + " " + r.URL.Path {
	case "POST /Users/New":
		var in struct{ Name string }
		json.NewDecoder(r.Body).Decode(&in)
		f.created = append(f.created, in.Name)
		json.NewEncoder(w).Encode(map[string]string{"Id": jfUser})
	case "GET /Users/" + jfUser:
		json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]any{"IsAdministrator": false}})
	case "POST /Users/" + jfUser + "/Policy":
		json.NewDecoder(r.Body).Decode(&f.policy)
		w.WriteHeader(http.StatusNoContent)
	case "POST /Users/AuthenticateByName":
		if f.failLogin {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"AccessToken": "user-token", "User": map[string]string{"Id": jfUser}})
	case "DELETE /Users/" + jfUser:
		f.deleted = append(f.deleted, jfUser)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T, fake *fakeJellyfin, group string) *Provisioner {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	jf, _ := jellyfin.New(srv.URL, "test")
	box, _ := seal.New(bytes.Repeat([]byte{5}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Provisioner{
		Store: st, Jellyfin: jf, AdminKey: "admin", RequiredGroup: group,
		LogKey: []byte("log-key"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestFirstVisitProvisionsOnce(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "media")
	id := identity.Identity{Subject: "subject-1", Groups: []string{"media"}}
	ctx := context.Background()

	acct, prof, err := p.Ensure(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !prof.Owner || prof.JellyfinToken != "user-token" || prof.Name != OwnerProfileName {
		t.Errorf("owner profile = %+v", prof)
	}
	if len(fake.created) != 1 || !strings.HasPrefix(fake.created[0], "of-") {
		t.Errorf("created users = %v, want one opaque name", fake.created)
	}
	if fake.policy["IsAdministrator"] != false || fake.policy["IsHidden"] != true {
		t.Errorf("policy not locked down: %v", fake.policy)
	}

	// Second visit: same account, no new Jellyfin user.
	acct2, _, err := p.Ensure(ctx, id)
	if err != nil || acct2.ID != acct.ID || len(fake.created) != 1 {
		t.Errorf("second visit: %v, %v, created %d users", acct2, err, len(fake.created))
	}
}

func TestRequiredGroup(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "media")
	_, _, err := p.Ensure(context.Background(), identity.Identity{Subject: "s", Groups: []string{"other"}})
	if !errors.Is(err, ErrNotAllowed) || len(fake.created) != 0 {
		t.Errorf("err = %v, created = %v", err, fake.created)
	}
}

func TestFailedLoginRollsBack(t *testing.T) {
	fake := &fakeJellyfin{failLogin: true}
	p := setup(t, fake, "")
	_, _, err := p.Ensure(context.Background(), identity.Identity{Subject: "s"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(fake.deleted) != 1 {
		t.Errorf("the Jellyfin user was not deleted after the failure")
	}
	if _, err := p.Store.AccountBySubject(context.Background(), "s"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an account was stored despite the failure: %v", err)
	}
}

func TestConcurrentFirstVisits(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	id := identity.Identity{Subject: "same-person"}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := p.Ensure(context.Background(), id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(fake.created) != 1 {
		t.Errorf("%d Jellyfin users created for one person, want 1", len(fake.created))
	}
}

func TestAnonymousHasNoAccount(t *testing.T) {
	p := setup(t, &fakeJellyfin{}, "")
	if _, _, err := p.Ensure(context.Background(), identity.Identity{Anonymous: true}); !errors.Is(err, ErrAnonymous) {
		t.Errorf("err = %v", err)
	}
}

func TestBucket(t *testing.T) {
	var b bucket
	now := time.Now()
	for i := 0; i < burst; i++ {
		if !b.allow(now) {
			t.Fatalf("request %d refused within the burst", i)
		}
	}
	if b.allow(now) {
		t.Error("burst exceeded")
	}
	if !b.allow(now.Add(interval)) {
		t.Error("no refill after one interval")
	}
}
