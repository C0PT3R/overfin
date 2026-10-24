package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

const (
	jfUser    = "b691e62a7c7244b6934a160398466052"
	aliceUser = "0123456789abcdef0123456789abcdef" // an existing account
	adminUser = "fedcba9876543210fedcba9876543210" // an existing administrator
	alicePass = "alice-password"
	adminPass = "admin-password"
)

// fakeJellyfin records what Overfin does, and can fail on demand.
type fakeJellyfin struct {
	mu        sync.Mutex
	created   []string
	deleted   []string
	policy    map[string]any
	failLogin bool
	logins    []string

	// alice's current name and password, which a move changes; failing
	// names a step that fails ("rename", "password").
	aliceName, alicePw string
	alicePolicy        map[string]any
	failing            string
	logouts            int
	// For the service account: passwords set, and whether its user is gone.
	passwords   int
	serviceGone bool
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.aliceName == "" {
		f.aliceName, f.alicePw = "alice", alicePass
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /System/Info/Public":
		json.NewEncoder(w).Encode(map[string]any{"Version": "12.2.0", "Id": "old-server", "StartupWizardCompleted": true})
	case "POST /Sessions/Logout":
		f.logouts++
		w.WriteHeader(http.StatusNoContent)
	case "POST /Users":
		if f.failing == "rename" || r.URL.Query().Get("userId") != aliceUser {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var in struct{ Name string }
		json.NewDecoder(r.Body).Decode(&in)
		f.aliceName = in.Name
		w.WriteHeader(http.StatusNoContent)
	case "POST /Users/Password":
		if r.URL.Query().Get("userId") == jfUser {
			if f.serviceGone {
				http.NotFound(w, r)
				return
			}
			f.passwords++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if f.failing == "password" || r.URL.Query().Get("userId") != aliceUser {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var in struct{ NewPw string }
		json.NewDecoder(r.Body).Decode(&in)
		f.alicePw = in.NewPw
		w.WriteHeader(http.StatusNoContent)
	case "POST /Users/" + aliceUser + "/Policy":
		f.alicePolicy = nil // a new policy replaces the old one entirely
		json.NewDecoder(r.Body).Decode(&f.alicePolicy)
		w.WriteHeader(http.StatusNoContent)
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
		var in struct{ Username, Pw string }
		json.NewDecoder(r.Body).Decode(&in)
		f.logins = append(f.logins, in.Username)
		id := jfUser
		switch {
		case in.Username == f.aliceName && in.Pw == f.alicePw:
			id = aliceUser
		case in.Username == "boss" && in.Pw == adminPass:
			id = adminUser
		case f.failLogin || !(strings.HasPrefix(in.Username, "of-") || strings.HasPrefix(in.Username, "overfin-service-")):
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-" + id, "User": map[string]string{"Id": id}})
	case "GET /Users/" + aliceUser:
		policy := f.alicePolicy
		if policy == nil {
			policy = map[string]any{"IsAdministrator": false, "MaxParentalRating": 10.0}
		}
		json.NewEncoder(w).Encode(map[string]any{"Name": f.aliceName, "Policy": policy})
	case "GET /Users/" + adminUser:
		json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]any{"IsAdministrator": true}})
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

	acct, prof, err := p.Ensure(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	// No Jellyfin user of its own: Overfin's one account serves everyone.
	if !prof.Owner || prof.JellyfinUserID != "" || prof.JellyfinToken != "" || prof.Name != OwnerProfileName {
		t.Errorf("owner profile = %+v", prof)
	}
	if len(fake.created) != 0 {
		t.Errorf("created users = %v, want none", fake.created)
	}

	// Second visit: same account.
	acct2, _, err := p.Ensure(ctx, id, nil)
	if err != nil || acct2.ID != acct.ID {
		t.Errorf("second visit: %v, %v", acct2, err)
	}
	// Another profile: none either.
	kid, err := p.AddProfile(ctx, acct.ID, store.NewProfile{Name: "Kid", Kind: "kids"}, 0)
	if err != nil || kid.JellyfinUserID != "" || len(fake.created) != 0 {
		t.Errorf("another profile: %+v %v, created %v", kid, err, fake.created)
	}
}

func TestRequiredGroup(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "media")
	_, _, err := p.Ensure(context.Background(), identity.Identity{Subject: "s", Groups: []string{"other"}}, nil)
	if !errors.Is(err, ErrNotAllowed) || len(fake.created) != 0 {
		t.Errorf("err = %v, created = %v", err, fake.created)
	}
}

func TestConcurrentFirstVisits(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	id := identity.Identity{Subject: "same-person"}

	var wg sync.WaitGroup
	accounts := make([]string, 5)
	for i := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acct, _, err := p.Ensure(context.Background(), id, nil)
			if err != nil {
				t.Error(err)
				return
			}
			accounts[i] = acct.ID
		}()
	}
	wg.Wait()
	for _, a := range accounts {
		if a != accounts[0] {
			t.Errorf("one person, several accounts: %v", accounts)
		}
	}
}

func TestAnonymousHasNoAccount(t *testing.T) {
	p := setup(t, &fakeJellyfin{}, "")
	if _, _, err := p.Ensure(context.Background(), identity.Identity{Anonymous: true}, nil); !errors.Is(err, ErrAnonymous) {
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

// history stands for personal.FromServer: what alice watched, as titles.
func history(_ context.Context, _ *jellyfin.Client, a jellyfin.Auth, userID string) ([]store.Personal, int, error) {
	if a.Token != "token-"+userID {
		return nil, 0, errors.New("read with someone else's token")
	}
	return []store.Personal{{Title: "movie/tmdb/949", Played: true, PlayCount: 2},
		{Title: "series/tmdb/1399/s1/e2", PositionMs: 60_000}}, 1, nil
}

// copyFrom is the provisioner's own fake Jellyfin standing for the other
// server.
func copyFrom(p *Provisioner, id identity.Identity, username, password string, hooks *Hooks) (*store.Account, *store.Profile, error) {
	return p.CopyIn(context.Background(), id, p.Jellyfin, username, password, history, hooks)
}

func TestCopyIn(t *testing.T) {
	ctx := context.Background()
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	id := identity.Identity{Subject: "alice-subject"}

	_, prof, err := copyFrom(p, id, "alice", alicePass, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prof.JellyfinUserID != "" || !prof.Owner {
		t.Errorf("profile = %+v", prof)
	}
	got, err := p.Store.PersonalStates(ctx, prof.ID, []string{"movie/tmdb/949", "series/tmdb/1399/s1/e2"})
	if err != nil || !got["movie/tmdb/949"].Played || got["movie/tmdb/949"].PlayCount != 2 || got["series/tmdb/1399/s1/e2"].PositionMs != 60_000 {
		t.Errorf("history = %+v, %v", got, err)
	}
	if imported, _ := p.Store.Imported(ctx, prof.ID); !imported {
		t.Error("the profile would be imported from this server's Jellyfin too")
	}
	// Nothing changed on the other server, and Overfin signed out there.
	if len(fake.created) != 0 || fake.aliceName != "alice" || fake.alicePw != alicePass || fake.alicePolicy != nil || fake.logouts != 1 {
		t.Errorf("the other server: created %v, alice %q %q %v, logouts %d", fake.created, fake.aliceName, fake.alicePw, fake.alicePolicy, fake.logouts)
	}

	// Copying again, or creating a fresh account, once one exists.
	if _, _, err := copyFrom(p, id, "alice", alicePass, nil); !errors.Is(err, ErrAccountExists) {
		t.Errorf("second copy: %v", err)
	}
	if _, _, err := p.Create(ctx, id, nil); !errors.Is(err, ErrAccountExists) {
		t.Errorf("create after a copy: %v", err)
	}
	// Someone else copying alice's history, with her password.
	if _, _, err := copyFrom(p, identity.Identity{Subject: "other"}, "alice", alicePass, nil); !errors.Is(err, ErrClaimNotAllowed) {
		t.Errorf("copying alice's history twice: %v", err)
	}
	if _, err := p.Store.AccountBySubject(ctx, "other"); !errors.Is(err, store.ErrNotFound) {
		t.Error("an account was made anyway")
	}
}

// Administrators may copy their history: the token is the other server's,
// used once, never Overfin's.
func TestCopyInAdministrators(t *testing.T) {
	p := setup(t, &fakeJellyfin{}, "")
	if _, _, err := copyFrom(p, identity.Identity{Subject: "s"}, "boss", adminPass, nil); err != nil {
		t.Errorf("an administrator's copy: %v", err)
	}
}

func TestCopyAttemptsAreLimited(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	id := identity.Identity{Subject: "guesser"}
	for i := 0; i < maxFailures; i++ {
		if _, _, err := copyFrom(p, id, "alice", "wrong", nil); !errors.Is(err, ErrClaimFailed) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Even the right password is refused now, without asking the server.
	before := len(fake.logins)
	if _, _, err := copyFrom(p, id, "alice", alicePass, nil); !errors.Is(err, ErrRateLimited) {
		t.Errorf("after %d failures: %v, want ErrRateLimited", maxFailures, err)
	}
	if len(fake.logins) != before {
		t.Error("the server was asked despite the limit")
	}
}

func TestCopyRespectsRequiredGroup(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "media")
	_, _, err := copyFrom(p, identity.Identity{Subject: "s"}, "alice", alicePass, nil)
	if !errors.Is(err, ErrNotAllowed) || len(fake.logins) != 0 {
		t.Errorf("err = %v, logins = %v", err, fake.logins)
	}
}

func TestRemovedFromTheGroup(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "media")
	ctx := context.Background()
	if _, _, err := p.Ensure(ctx, identity.Identity{Subject: "s", Groups: []string{"media"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Find(ctx, identity.Identity{Subject: "s", Groups: []string{"other"}}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("out of the group, still in: %v", err)
	}
}

func TestExpiredAccount(t *testing.T) {
	ctx := context.Background()
	p := setup(t, &fakeJellyfin{}, "")
	acct, _, err := p.Ensure(ctx, identity.Identity{Subject: "s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Store.SetAccountExpiry(ctx, acct.ID, time.Now().Add(-time.Minute))
	if _, _, err := p.Find(ctx, identity.Identity{Subject: "s"}); !errors.Is(err, ErrExpired) {
		t.Errorf("expired account: %v", err)
	}
	_ = p.Store.SetAccountExpiry(ctx, acct.ID, time.Time{})
	if _, _, err := p.Find(ctx, identity.Identity{Subject: "s"}); err != nil {
		t.Errorf("after clearing the expiry: %v", err)
	}
}

func TestCopyHooks(t *testing.T) {
	ctx := context.Background()
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")

	// Refused before the other server is even asked.
	refuse := &Hooks{Before: func(context.Context) error { return errors.New("no invitation") }}
	if _, _, err := copyFrom(p, identity.Identity{Subject: "s"}, "alice", alicePass, refuse); err == nil {
		t.Fatal("the copy went through a refusal")
	}
	if len(fake.logins) != 0 {
		t.Errorf("the other server was asked despite the refusal: %v", fake.logins)
	}

	// A failure afterwards undoes it all, the record of the copy included.
	fail := &Hooks{After: func(context.Context, *store.Account, *store.Profile) error {
		return errors.New("invitation used meanwhile")
	}}
	if _, _, err := copyFrom(p, identity.Identity{Subject: "s"}, "alice", alicePass, fail); err == nil {
		t.Fatal("the copy succeeded")
	}
	if _, err := p.Store.AccountBySubject(ctx, "s"); !errors.Is(err, store.ErrNotFound) {
		t.Error("an account was left behind")
	}
	// And it can be done properly afterwards.
	if _, _, err := copyFrom(p, identity.Identity{Subject: "s"}, "alice", alicePass, nil); err != nil {
		t.Errorf("a copy after the undo: %v", err)
	}
}

// A deleted account's Seerr user goes with it, unless its history is kept.
func TestDeletingAnAccountDeletesItsSeerrUser(t *testing.T) {
	p := setup(t, &fakeJellyfin{}, "")
	ctx := context.Background()
	var deleted []int
	p.DeleteSeerrUser = func(_ context.Context, id int) error {
		deleted = append(deleted, id)
		return nil
	}
	for i, keep := range []bool{true, false} {
		acct, _, err := p.Ensure(ctx, identity.Identity{Subject: fmt.Sprint("subject-", i)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		p.Store.SetSeerrUser(ctx, acct.ID, 40+i, time.Now())
		if _, err := p.DeleteAccount(ctx, acct.ID, keep); err != nil {
			t.Fatal(err)
		}
	}
	if len(deleted) != 1 || deleted[0] != 41 {
		t.Errorf("Seerr users deleted: %v", deleted)
	}
}

// A new account comes back whole: with its template's limits.
func TestCreateGivesTemplate(t *testing.T) {
	ctx := context.Background()
	p := setup(t, &fakeJellyfin{}, "")
	acct, _, err := p.Create(ctx, identity.Identity{Subject: "me"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Template.ID != store.DefaultTemplate || acct.Template.MaxProfiles == 0 || acct.Template.MaxStreams == 0 {
		t.Errorf("template: %+v", acct.Template)
	}
}

// Someone with an account brings their history into the profile they're
// on, once, merged with what's there; kids profiles can't.
func TestCopyInto(t *testing.T) {
	ctx := context.Background()
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	acct, owner, err := p.Create(ctx, identity.Identity{Subject: "me"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// What the profile did here already stays.
	if err := p.Store.MergePersonal(ctx, owner.ID, []store.Personal{{Title: "movie/tmdb/949", PlayCount: 5}}); err != nil {
		t.Fatal(err)
	}
	kids := &store.Profile{ID: owner.ID, Kind: "kids"}
	if err := p.CopyInto(ctx, "me", acct.ID, kids, p.Jellyfin, "alice", alicePass, history); !errors.Is(err, ErrClaimNotAllowed) || len(fake.logins) != 0 {
		t.Errorf("into a kids profile: %v, logins %v", err, fake.logins)
	}
	if err := p.CopyInto(ctx, "me", acct.ID, owner, p.Jellyfin, "alice", alicePass, history); err != nil {
		t.Fatal(err)
	}
	got, _ := p.Store.PersonalStates(ctx, owner.ID, []string{"movie/tmdb/949", "series/tmdb/1399/s1/e2"})
	if !got["movie/tmdb/949"].Played || got["movie/tmdb/949"].PlayCount != 5 || got["series/tmdb/1399/s1/e2"].PositionMs != 60_000 {
		t.Errorf("merged: %+v", got)
	}
	if fake.logouts != 1 {
		t.Errorf("signed out %d times", fake.logouts)
	}
	// Once per account: another login, even a right one, is refused…
	if err := p.CopyInto(ctx, "me", acct.ID, owner, p.Jellyfin, "boss", adminPass, history); !errors.Is(err, ErrClaimNotAllowed) {
		t.Errorf("a second copy: %v", err)
	}
	// …until an admin allows it again; alice's account is free again too.
	if n, err := p.Store.ForgetCopies(ctx, acct.ID); err != nil || n != 1 {
		t.Fatalf("ForgetCopies = %d, %v", n, err)
	}
	if _, _, err := copyFrom(p, identity.Identity{Subject: "someone"}, "alice", alicePass, nil); err != nil {
		t.Errorf("alice's history, freed: %v", err)
	}
	if err := p.CopyInto(ctx, "me", acct.ID, owner, p.Jellyfin, "boss", adminPass, history); err != nil {
		t.Errorf("allowed again: %v", err)
	}
}
