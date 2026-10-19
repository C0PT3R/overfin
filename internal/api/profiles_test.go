package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSessionCookie(t *testing.T) {
	srv, _ := newServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/me", nil)
	req.Header.Set("Remote-User", subject)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	c := res.Header.Get("Set-Cookie")
	for _, want := range []string{"__Host-overfin=", "Path=/", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(c, want) {
			t.Errorf("cookie lacks %q: %s", want, c)
		}
	}
	if strings.Contains(c, "Domain=") {
		t.Errorf("cookie has a Domain: %s", c)
	}
}

// A cookie only counts for the account it was made for.
func TestStolenCookieIsIgnored(t *testing.T) {
	srv, _ := newServer(t)
	getAs(t, srv, subject, "/api/v1/me") // gives the test subject a session
	stolen := client(srv, subject).Jar.Cookies(mustURL(srv.URL))

	thief := &http.Client{Transport: srv.Client().Transport}
	thief.Jar, _ = cookiejar.New(nil)
	thief.Jar.SetCookies(mustURL(srv.URL), stolen)
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/sessions", nil)
	req.Header.Set("Remote-User", "other-subject")
	res, err := thief.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Items []struct{ ID string } }
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if len(out.Items) != 1 {
		t.Fatalf("other account's sessions: %+v", out)
	}
	_, mine := getAs(t, srv, subject, "/api/v1/sessions")
	if strings.Contains(mine, out.Items[0].ID) {
		t.Error("the stolen cookie gave access to the victim's session")
	}
}

func TestProfilesAndSwitching(t *testing.T) {
	srv, fake := newServer(t)

	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var partner struct{ ID, Name string }
	json.Unmarshal([]byte(body), &partner)
	if code, _ := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`); code != http.StatusConflict {
		t.Errorf("same name twice: %d", code)
	}
	for _, bad := range []string{`{"name":""}`, `{"name":"<b>x</b>"}`, `{"name":"x","kind":"kids"}`, `{"name":"x","pin":"1234"}`, `{"name":"` + strings.Repeat("a", 40) + `"}`} {
		if code, _ := post(t, srv, "/api/v1/profiles", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}

	// Switch: the library now runs with the partner's own Jellyfin token.
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}
	_, me := getAs(t, srv, subject, "/api/v1/me")
	if !strings.Contains(me, partner.ID) {
		t.Errorf("active profile after switching: %s", me)
	}
	getAs(t, srv, subject, "/api/v1/libraries")
	if fake.lastToken != partnerToken {
		t.Errorf("library call used %q, want the partner's token", fake.lastToken)
	}

	// The partner profile isn't the owner: no managing profiles or sessions.
	if code, _ := post(t, srv, "/api/v1/profiles", `{"name":"Third"}`); code != http.StatusForbidden {
		t.Errorf("non-owner created a profile: %d", code)
	}
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/sessions", nil)
	req.Header.Set("Remote-User", subject)
	res, _ := client(srv, subject).Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("non-owner ended other sessions: %d", res.StatusCode)
	}
	if code, list := getAs(t, srv, subject, "/api/v1/profiles"); code != 200 || !strings.Contains(list, `"Partner"`) || !strings.Contains(list, `"Main"`) {
		t.Errorf("profile list: %d %s", code, list)
	}
}

func put(t *testing.T, srv *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Content-Type", "application/json")
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestKidsProfiles(t *testing.T) {
	srv, fake := newServer(t)

	if code, body := getAs(t, srv, subject, "/api/v1/ratings"); code != 200 || !strings.Contains(body, `[{"name":"G"},{"name":"PG"},{"name":"PG-13"}]`) {
		t.Errorf("ratings, least mature first: %d %s", code, body)
	}
	for _, bad := range []string{`{"name":"Kid","kind":"kids"}`, `{"name":"Kid","kind":"kids","ratingCeiling":"X"}`,
		`{"name":"Kid","ratingCeiling":"PG"}`, `{"name":"Kid","kind":"teen","ratingCeiling":"PG"}`} {
		if code, _ := post(t, srv, "/api/v1/profiles", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","ratingCeiling":"PG"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"ratingCeiling":"PG"`) {
		t.Fatalf("create: %d %s", code, body)
	}
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)
	// The limit is in the same policy write as the lock-down.
	if fake.policy["MaxParentalRating"] != 10.0 || fake.policy["IsHidden"] != true || fake.policy["BlockUnratedItems"] == nil {
		t.Errorf("kids policy: %v", fake.policy)
	}

	// The owner's PIN guards limits once the owner has one.
	if code, body := put(t, srv, "PUT", "/api/v1/profiles/"+ownerID(t, srv)+"/pin", `{"pin":"1234"}`); code != 204 {
		t.Fatalf("set owner PIN: %d %s", code, body)
	}
	if code, body := put(t, srv, "PUT", "/api/v1/profiles/"+ownerID(t, srv)+"/pin", `{"pin":"9999"}`); code != 403 || !strings.Contains(body, "step-up-required") {
		t.Errorf("changing a PIN: %d %s", code, body)
	}
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+kid.ID, `{"ratingCeiling":"PG-13"}`); code != 403 || !strings.Contains(body, "pin-required") {
		t.Errorf("raise without PIN: %d %s", code, body)
	}
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+kid.ID, `{"ratingCeiling":"PG-13","pin":"0000"}`); code != 403 || !strings.Contains(body, "pin-incorrect") {
		t.Errorf("raise with a wrong PIN: %d %s", code, body)
	}
	// Right PIN, but too soon after a wrong one.
	code, body = put(t, srv, "PATCH", "/api/v1/profiles/"+kid.ID, `{"ratingCeiling":"PG-13","pin":"1234"}`)
	var wait struct{ RetryAfter int }
	json.Unmarshal([]byte(body), &wait)
	if code != 429 || wait.RetryAfter < 1 || wait.RetryAfter > 2 {
		t.Errorf("right PIN too soon: %d %s", code, body)
	}
	time.Sleep(time.Duration(wait.RetryAfter) * time.Second)
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+kid.ID, `{"ratingCeiling":"PG-13","pin":"1234"}`); code != 200 || !strings.Contains(body, `"PG-13"`) {
		t.Errorf("raise: %d %s", code, body)
	}
	if fake.policy["MaxParentalRating"] != 13.0 {
		t.Errorf("policy after raising: %v", fake.policy)
	}
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+kid.ID, `{"kind":"standard","pin":"1234"}`); code != 200 || strings.Contains(body, "ratingCeiling") {
		t.Errorf("graduate: %d %s", code, body)
	}
	if fake.policy["MaxParentalRating"] != nil {
		t.Errorf("policy after graduating: %v", fake.policy)
	}
}

func TestSwitchingNeedsThePIN(t *testing.T) {
	srv, _ := newServer(t)
	owner := ownerID(t, srv)
	put(t, srv, "PUT", "/api/v1/profiles/"+owner+"/pin", `{"pin":"1234"}`)
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","ratingCeiling":"G"}`)
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)

	// Into an unprotected profile: no PIN.
	if code, body := post(t, srv, "/api/v1/profiles/"+kid.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate kid: %d %s", code, body)
	}
	// A kids profile can't lock itself, nor manage another's PIN.
	if code, _ := put(t, srv, "PUT", "/api/v1/profiles/"+kid.ID+"/pin", `{"pin":"5555"}`); code != 403 {
		t.Errorf("kid set its own PIN: %d", code)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+owner+"/activate", `{}`); code != 403 || !strings.Contains(body, "pin-required") {
		t.Errorf("back to the owner without PIN: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+owner+"/activate", `{"pin":"1234"}`); code != 200 {
		t.Errorf("back to the owner with PIN: %d %s", code, body)
	}
}

func ownerID(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	_, body := getAs(t, srv, subject, "/api/v1/profiles")
	var out struct {
		Items []struct {
			ID    string
			Owner bool
		}
	}
	json.Unmarshal([]byte(body), &out)
	for _, p := range out.Items {
		if p.Owner {
			return p.ID
		}
	}
	t.Fatal("no owner profile")
	return ""
}

// A stream URL only works in the session that started the playback.
func TestTicketBoundToSession(t *testing.T) {
	srv, _ := newServer(t)
	_, body := post(t, srv, "/api/v1/playback", `{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{}}`)
	var s struct{ Stream struct{ URL string } }
	json.Unmarshal([]byte(body), &s)
	if code, _ := getAs(t, srv, subject, s.Stream.URL); code != 200 {
		t.Fatalf("own session: %d", code)
	}
	// Same person, another browser (a fresh cookie jar): no access.
	other := &http.Client{Transport: srv.Client().Transport}
	other.Jar, _ = cookiejar.New(nil)
	req, _ := http.NewRequest("GET", srv.URL+s.Stream.URL, nil)
	req.Header.Set("Remote-User", subject)
	res, err := other.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("another session used the stream URL: %d", res.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	srv, _ := newServer(t)
	getAs(t, srv, subject, "/api/v1/me")
	code, body := post(t, srv, "/api/v1/logout", `{}`)
	if code != 200 || !strings.Contains(body, `"logoutUrl":"/sign-out"`) {
		t.Errorf("logout: %d %s", code, body)
	}
	if n := len(client(srv, subject).Jar.Cookies(mustURL(srv.URL))); n != 0 {
		t.Errorf("%d cookies left after logout", n)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
