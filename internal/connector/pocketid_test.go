package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// People: every page, by user ID; the display name, else first and last
// names; disabled people inactive. A wrong key is told apart.
func TestPocketIDPeople(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "good-key-1234567" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/users" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("pagination[page]") {
		case "1":
			io.WriteString(w, `{"data":[{"id":"u1","username":"alice","email":"a@x.org","firstName":"Alice","lastName":"Roy","displayName":""},
				{"id":"u2","username":"bob","email":null,"firstName":"","displayName":"Bobby","disabled":true}],
				"pagination":{"totalPages":2,"currentPage":1}}`)
		default:
			fmt.Fprint(w, `{"data":[{"id":"u3","username":"carol","displayName":"Carol"}],"pagination":{"totalPages":2,"currentPage":2}}`)
		}
	}))
	defer srv.Close()
	p := &PocketID{BaseURL: srv.URL + "/", Key: "good-key-1234567", HTTP: srv.Client()}
	people, err := p.People(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 3 || people["u1"].Name != "Alice Roy" || people["u1"].Email != "a@x.org" || !people["u1"].Active ||
		people["u2"].Name != "Bobby" || people["u2"].Active || people["u3"].Username != "carol" {
		t.Errorf("people: %+v", people)
	}
	bad := &PocketID{BaseURL: srv.URL, Key: "wrong", HTTP: srv.Client()}
	if err := bad.Check(context.Background()); !errors.Is(err, ErrPocketIDKey) {
		t.Errorf("wrong key: %v", err)
	}
}

// SignupToken: groups by name become their IDs; one use, the TTL in
// seconds; a missing group is an error, not a token without it.
func TestPocketIDSignupToken(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/user-groups":
			io.WriteString(w, `{"data":[{"id":"g-other","name":"members-old"},{"id":"g1","name":"members"}]}`)
		case r.URL.Path == "/api/signup-tokens" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&got)
			io.WriteString(w, `{"id":"t1","token":"abc123"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &PocketID{BaseURL: srv.URL, Key: "k", HTTP: srv.Client()}
	token, err := p.SignupToken(context.Background(), 24*time.Hour, []string{"members"})
	if err != nil || token != "abc123" {
		t.Fatalf("token %q, %v", token, err)
	}
	if fmt.Sprint(got) != "map[ttl:86400 usageLimit:1 userGroupIds:[g1]]" {
		t.Errorf("sent %v", got)
	}
	if _, err := p.SignupToken(context.Background(), time.Hour, []string{"nobody"}); err == nil {
		t.Error("a missing group gave a token")
	}
}

// FirstAdmin: made only while Pocket ID has nobody but its API key's user,
// as an admin, in the groups.
func TestPocketIDFirstAdmin(t *testing.T) {
	users := `{"data":[{"id":"00000000-0000-0000-0000-000000000000"}]}`
	var made map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/user-groups":
			io.WriteString(w, `{"data":[{"id":"g1","name":"admins"}]}`)
		case r.URL.Path == "/api/users" && r.Method == http.MethodGet:
			io.WriteString(w, users)
		case r.URL.Path == "/api/users" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&made)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &PocketID{BaseURL: srv.URL, Key: "k", HTTP: srv.Client()}
	ok, err := p.FirstAdmin(context.Background(), "me@example.org", []string{"admins"})
	if err != nil || !ok {
		t.Fatalf("made %v, %v", ok, err)
	}
	if fmt.Sprint(made) != "map[email:me@example.org emailVerified:true isAdmin:true userGroupIds:[g1] username:me@example.org]" {
		t.Errorf("sent %v", made)
	}
	users, made = `{"data":[{"id":"00000000-0000-0000-0000-000000000000"},{"id":"u1"}]}`, nil
	if ok, err := p.FirstAdmin(context.Background(), "me@example.org", []string{"admins"}); err != nil || ok || made != nil {
		t.Errorf("with someone there: made %v, %v, sent %v", ok, err, made)
	}
}
