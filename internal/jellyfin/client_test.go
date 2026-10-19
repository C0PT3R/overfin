package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const userID = "b691e62a7c7244b6934a160398466052"

func TestNormalizeID(t *testing.T) {
	for in, want := range map[string]string{
		"b691e62a-7c72-44b6-934a-160398466052": userID,
		"B691E62A7C7244B6934A160398466052":     userID,
	} {
		if got, err := NormalizeID(in); err != nil || got != want {
			t.Errorf("NormalizeID(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "../Users", "b691e62a", userID + "0", "zz91e62a7c7244b6934a160398466052"} {
		if _, err := NormalizeID(bad); err == nil {
			t.Errorf("NormalizeID(%q) accepted", bad)
		}
	}
}

func TestAuthHeaderAndUserCalls(t *testing.T) {
	var gotAuth string
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.Method + " " + r.URL.Path {
		case "POST /Users/New":
			json.NewEncoder(w).Encode(map[string]string{"Id": "b691e62a-7c72-44b6-934a-160398466052"})
		case "GET /Users/" + userID:
			json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]any{
				"IsAdministrator": true, "AuthenticationProviderId": "keep-me",
			}})
		case "POST /Users/" + userID + "/Policy":
			json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "test")
	admin := Auth{Token: "admin-key", DeviceID: "overfin-admin"}
	ctx := context.Background()

	id, err := c.CreateUser(ctx, admin, "of-test", "pw")
	if err != nil || id != userID {
		t.Fatalf("CreateUser = %q, %v", id, err)
	}
	if !strings.HasPrefix(gotAuth, "MediaBrowser ") || !strings.Contains(gotAuth, `Token="admin-key"`) {
		t.Errorf("Authorization header = %q", gotAuth)
	}

	p, err := c.UserPolicy(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	p.LockDown()
	if p["IsAdministrator"] != false || p["IsHidden"] != true || p["AuthenticationProviderId"] != "keep-me" {
		t.Errorf("LockDown result: %v", p)
	}
	if err := c.SetUserPolicy(ctx, admin, id, p); err != nil {
		t.Fatal(err)
	}
	// What Jellyfin receives: locked down, and its own fields kept.
	if posted["IsAdministrator"] != false || posted["EnableContentDeletion"] != false ||
		posted["AuthenticationProviderId"] != "keep-me" {
		t.Errorf("posted policy: %v", posted)
	}
}

func TestNoRedirectsAndNoErrorText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/New" {
			http.Redirect(w, r, "http://198.51.100.1/steal", http.StatusFound)
			return
		}
		http.Error(w, "internal secret detail", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "test")

	_, err := c.CreateUser(context.Background(), Auth{}, "x", "y")
	if !IsStatus(err, http.StatusFound) {
		t.Errorf("redirect was followed or misreported: %v", err)
	}
	err = c.DeleteUser(context.Background(), Auth{}, userID)
	if !IsStatus(err, http.StatusInternalServerError) || strings.Contains(err.Error(), "secret") {
		t.Errorf("error = %v", err)
	}
}

func TestRejectsMalformedIDFromUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"Id": "../../System"})
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "test")
	if _, err := c.CreateUser(context.Background(), Auth{}, "x", "y"); err == nil {
		t.Error("a malformed ID from upstream was accepted")
	}
}
