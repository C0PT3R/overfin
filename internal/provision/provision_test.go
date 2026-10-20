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
		case f.failLogin || !strings.HasPrefix(in.Username, "of-"):
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
	if !prof.Owner || prof.JellyfinToken != "token-"+jfUser || prof.Name != OwnerProfileName {
		t.Errorf("owner profile = %+v", prof)
	}
	if len(fake.created) != 1 || !strings.HasPrefix(fake.created[0], "of-") {
		t.Errorf("created users = %v, want one opaque name", fake.created)
	}
	if fake.policy["IsAdministrator"] != false || fake.policy["IsHidden"] != true {
		t.Errorf("policy not locked down: %v", fake.policy)
	}

	// Second visit: same account, no new Jellyfin user.
	acct2, _, err := p.Ensure(ctx, id, nil)
	if err != nil || acct2.ID != acct.ID || len(fake.created) != 1 {
		t.Errorf("second visit: %v, %v, created %d users", acct2, err, len(fake.created))
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

func TestFailedLoginRollsBack(t *testing.T) {
	fake := &fakeJellyfin{failLogin: true}
	p := setup(t, fake, "")
	_, _, err := p.Ensure(context.Background(), identity.Identity{Subject: "s"}, nil)
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
			if _, _, err := p.Ensure(context.Background(), id, nil); err != nil {
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

	_, prof, err := p.Claim(ctx, id, "alice", alicePass, nil)
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
	if _, _, err := p.Claim(ctx, id, "alice", alicePass, nil); !errors.Is(err, ErrAccountExists) {
		t.Errorf("second claim: %v", err)
	}
	if _, _, err := p.Create(ctx, id, nil); !errors.Is(err, ErrAccountExists) {
		t.Errorf("create after claim: %v", err)
	}
	// Someone else claiming alice's Jellyfin account, with her password.
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "other"}, "alice", alicePass, nil); !errors.Is(err, ErrClaimNotAllowed) {
		t.Errorf("claiming an adopted account: %v", err)
	}
}

func TestClaimRefusesAdministrators(t *testing.T) {
	p := setup(t, &fakeJellyfin{}, "")
	_, _, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "boss", adminPass, nil)
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
		if _, _, err := p.Claim(context.Background(), id, "alice", "wrong", nil); !errors.Is(err, ErrClaimFailed) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Even the right password is refused now, without asking Jellyfin.
	before := len(fake.logins)
	if _, _, err := p.Claim(context.Background(), id, "alice", alicePass, nil); !errors.Is(err, ErrRateLimited) {
		t.Errorf("after %d failures: %v, want ErrRateLimited", maxFailures, err)
	}
	if len(fake.logins) != before {
		t.Error("Jellyfin was asked despite the limit")
	}
}

func TestClaimRespectsRequiredGroup(t *testing.T) {
	fake := &fakeJellyfin{}
	p := setup(t, fake, "media")
	_, _, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "alice", alicePass, nil)
	if !errors.Is(err, ErrNotAllowed) || len(fake.logins) != 0 {
		t.Errorf("err = %v, logins = %v", err, fake.logins)
	}
}

func TestClaimMoves(t *testing.T) {
	ctx := context.Background()
	fake := &fakeJellyfin{}
	p := setup(t, fake, "")
	p.MoveOnClaim = true

	_, prof, err := p.Claim(ctx, identity.Identity{Subject: "alice-subject"}, "alice", alicePass, nil)
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
			p.MoveOnClaim = true
			if _, _, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "alice", alicePass, nil); err == nil {
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
	_, prof, err := p.Claim(context.Background(), identity.Identity{Subject: "s"}, "alice", alicePass, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.aliceName != "alice" || fake.alicePw != alicePass || prof.JellyfinUsername != "alice" {
		t.Errorf("link changed the login: %q %q", fake.aliceName, fake.alicePw)
	}
	// Moved later, without the old password.
	name, token, err := p.MoveLinked(context.Background(), aliceUser)
	if err != nil || !strings.HasPrefix(name, "of-") || token == "" || fake.alicePw == alicePass {
		t.Errorf("move later: %q %q %v", name, token, err)
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
	p.MoveOnClaim = true

	// Refused before the old account is even looked at.
	refuse := &Hooks{Before: func(context.Context) error { return errors.New("no invitation") }}
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "s"}, "alice", alicePass, refuse); err == nil {
		t.Fatal("claim went through a refusal")
	}
	if len(fake.logins) != 0 {
		t.Errorf("the old account was checked despite the refusal: %v", fake.logins)
	}

	// A failure afterwards undoes it all: the old account is as it was.
	fail := &Hooks{After: func(context.Context, *store.Account, *store.Profile) error {
		return errors.New("invitation used meanwhile")
	}}
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "s"}, "alice", alicePass, fail); err == nil {
		t.Fatal("claim succeeded")
	}
	if fake.aliceName != "alice" || fake.alicePw != alicePass || fake.alicePolicy["IsHidden"] == true {
		t.Errorf("old account not restored: %q %q %v", fake.aliceName, fake.alicePw, fake.alicePolicy)
	}
	if _, err := p.Store.AccountBySubject(ctx, "s"); !errors.Is(err, store.ErrNotFound) {
		t.Error("an account was left behind")
	}
	// And it can be claimed properly afterwards.
	if _, _, err := p.Claim(ctx, identity.Identity{Subject: "s"}, "alice", alicePass, nil); err != nil {
		t.Errorf("claim after the undo: %v", err)
	}
}
