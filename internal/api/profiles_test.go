package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
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
	for _, bad := range []string{`{"name":""}`, `{"name":"<b>x</b>"}`, `{"name":"x","kind":"kids"}`, `{"name":"` + strings.Repeat("a", 40) + `"}`} {
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
