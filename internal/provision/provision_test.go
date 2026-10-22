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

func TestClaim(t *testing.T) {
	ctx := context.Background()
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	id := identity.Identity{Subject: "alice-subject"}

	_, prof, err := p.Claim(ctx, id, "alice", alicePass, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prof.JellyfinUserID != aliceUser || prof.JellyfinToken != "token-"+aliceUser || !prof.Owner {
		t.Errorf("adopted profile = %+v", prof)
	}
	if len(fake.created) != 0 {
		t.Error("claiming created a new Jellyfin user")
	}

	// Claiming again, or creating a fresh account, once one exists.
	if _, _, err := p.Claim(ctx, id, "alice", alicePass, false, nil); !errors.Is(err, ErrAccountExists) {
		t.Errorf("second claim: %v", err)
	}
	if _, _, err := p.Create(ctx, id, nil); !errors.Is(err, ErrAccountExists) {
		t.Errorf("create after claim: %v", err)
	}
	// Someone else claiming alice's Jellyfin account, with her password.
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "other"}, "alice", alicePass, false, nil); !errors.Is(err, ErrClaimNotAllowed) {
		t.Errorf("claiming an adopted account: %v", err)
	}
}

func TestClaimRefusesAdministrators(t *testing.T) {
	p := setup(t, &fakeJellyfin{}, "")
	_, _, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "boss", adminPass, false, nil)
	if !errors.Is(err, ErrClaimNotAllowed) {
		t.Errorf("err = %v, want ErrClaimNotAllowed", err)
	}
	if _, err := p.Store.AccountBySubject(context.Background(), "s"); !errors.Is(err, store.ErrNotFound) {
		t.Error("an account was created for an administrator claim")
	}
}

func TestClaimAttemptsAreLimited(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	id := identity.Identity{Subject: "guesser"}
	for i := 0; i < maxFailures; i++ {
		if _, _, err := p.Claim(context.Background(), id, "alice", "wrong", false, nil); !errors.Is(err, ErrClaimFailed) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Even the right password is refused now, without asking Jellyfin.
	before := len(fake.logins)
	if _, _, err := p.Claim(context.Background(), id, "alice", alicePass, false, nil); !errors.Is(err, ErrRateLimited) {
		t.Errorf("after %d failures: %v, want ErrRateLimited", maxFailures, err)
	}
	if len(fake.logins) != before {
		t.Error("Jellyfin was asked despite the limit")
	}
}

func TestClaimRespectsRequiredGroup(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "media")
	_, _, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "alice", alicePass, false, nil)
	if !errors.Is(err, ErrNotAllowed) || len(fake.logins) != 0 {
		t.Errorf("err = %v, logins = %v", err, fake.logins)
	}
}

func TestClaimMoves(t *testing.T) {
	ctx := context.Background()
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")

	_, prof, err := p.Claim(ctx, identity.Identity{Subject: "alice-subject"}, "alice", alicePass, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An opaque name, the lock-down, a password nobody knows: the old login
	// is gone, and the profile holds the new name and a working token.
	if !strings.HasPrefix(fake.aliceName, "of-") || prof.JellyfinUsername != fake.aliceName {
		t.Errorf("name: jellyfin %q, profile %q", fake.aliceName, prof.JellyfinUsername)
	}
	if fake.alicePw == alicePass {
		t.Error("the old password still works")
	}
	if fake.alicePolicy["IsHidden"] != true || fake.alicePolicy["MaxParentalRating"] != 10.0 {
		t.Errorf("policy not locked down, or other settings lost: %v", fake.alicePolicy)
	}
	if prof.JellyfinToken != "token-"+aliceUser {
		t.Errorf("token = %q", prof.JellyfinToken)
	}
	if fake.logouts != 1 {
		t.Errorf("the claim check's session was ended %d times", fake.logouts)
	}
}

// A move that fails half-way puts everything back: the user can still log
// in as before, and try again.
func TestClaimMoveFailureRestores(t *testing.T) {
	for _, step := range []string{"rename", "password"} {
		t.Run(step, func(t *testing.T) {
			fake := &fakeJellyfin{failing: step}
			p := setup(t, fake, "")
			if _, _, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "alice", alicePass, true, nil); err == nil {
				t.Fatal("claim succeeded")
			}
			if fake.aliceName != "alice" || fake.alicePw != alicePass {
				t.Errorf("not restored: %q / %q", fake.aliceName, fake.alicePw)
			}
			if fake.alicePolicy["IsHidden"] == true {
				t.Errorf("lock-down left in place: %v", fake.alicePolicy)
			}
			if _, err := p.Store.AccountBySubject(context.Background(), "s"); !errors.Is(err, store.ErrNotFound) {
				t.Error("an account was created")
			}
		})
	}
}

func TestClaimLinkKeepsTheLogin(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	_, prof, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "alice", alicePass, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.aliceName != "alice" || fake.alicePw != alicePass || prof.JellyfinUsername != "alice" {
		t.Errorf("link changed the login: %q %q", fake.aliceName, fake.alicePw)
	}
	// Moved later, without the old password.
	moved, err := p.MoveLinked(context.Background())
	if err != nil || moved != 1 || !strings.HasPrefix(fake.aliceName, "of-") || fake.alicePw == alicePass {
		t.Errorf("move later: %d %q %v", moved, fake.aliceName, err)
	}
	prof, _ = p.Store.Profile(context.Background(), prof.AccountID, prof.ID)
	if prof.JellyfinUsername != fake.aliceName || prof.JellyfinToken == "" {
		t.Errorf("profile after the move: %q %q", prof.JellyfinUsername, prof.JellyfinToken)
	}
	if moved, err := p.MoveLinked(context.Background()); err != nil || moved != 0 {
		t.Errorf("moving again: %d %v", moved, err)
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

func TestClaimHooks(t *testing.T) {
	ctx := context.Background()
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")

	// Refused before the old account is even looked at.
	refuse := &Hooks{Before: func(context.Context) error { return errors.New("no invitation") }}
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "s"}, "alice", alicePass, true, refuse); err == nil {
		t.Fatal("claim went through a refusal")
	}
	if len(fake.logins) != 0 {
		t.Errorf("the old account was checked despite the refusal: %v", fake.logins)
	}

	// A failure afterwards undoes it all: the old account is as it was.
	fail := &Hooks{After: func(context.Context, *store.Account, *store.Profile) error {
		return errors.New("invitation used meanwhile")
	}}
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "s"}, "alice", alicePass, true, fail); err == nil {
		t.Fatal("claim succeeded")
	}
	if fake.aliceName != "alice" || fake.alicePw != alicePass || fake.alicePolicy["IsHidden"] == true {
		t.Errorf("old account not restored: %q %q %v", fake.aliceName, fake.alicePw, fake.alicePolicy)
	}
	if _, err := p.Store.AccountBySubject(ctx, "s"); !errors.Is(err, store.ErrNotFound) {
		t.Error("an account was left behind")
	}
	// And it can be claimed properly afterwards.
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "s"}, "alice", alicePass, true, nil); err != nil {
		t.Errorf("claim after the undo: %v", err)
	}
}
