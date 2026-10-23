package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newSetup is a setup page with a Jellyfin step like Overfin's: a server at
// an address with "fresh" in it is set up; one with "other" is handed over
// by its administrator (password "pw") once its ID is confirmed; one with
// "down" doesn't answer; any other fails.
func newSetup() (*Setup, *[]string) {
	var saved []string
	s := &Setup{Code: "K7QM-2XRP", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jellyfin: func(_ context.Context, req JellyfinRequest) (JellyfinAnswer, error) {
			ans := JellyfinAnswer{Server: JellyfinServer{ID: "srv-2", Version: "12.2.0"}}
			switch {
			case strings.Contains(req.URL, "down"):
				return ans, Problem("Jellyfin can't be reached.")
			case strings.Contains(req.URL, "other"):
				switch {
				case req.Username == "":
					ans.NeedsLogin = true
					return ans, nil
				case req.Password != "pw":
					return ans, Problem("Jellyfin refuses that username and password.")
				case req.Confirm != "srv-2":
					ans.Survey = &JellyfinSurvey{Users: []string{"admin", "bob"}, Libraries: 3}
					return ans, nil
				}
			case !strings.Contains(req.URL, "fresh"):
				return ans, errors.New("disk full")
			}
			saved = append(saved, req.URL)
			return JellyfinAnswer{Done: true, Summary: "Jellyfin 12.2.0, at " + req.URL + ": set up."}, nil
		}}
	return s, &saved
}

func post(h http.Handler, peer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/admin/api/setup", strings.NewReader(body))
	req.RemoteAddr = peer
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSetup(t *testing.T) {
	s, saved := newSetup()
	h := s.Handler()
	const local = "127.0.0.1:50000"

	// The page itself, on a private address only.
	for peer, want := range map[string]int{local: http.StatusOK, "203.0.113.5:4000": http.StatusNotFound} {
		req := httptest.NewRequest("GET", "/admin/", nil)
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want || (want == 200 && !strings.Contains(rec.Body.String(), "Set up Overfin")) {
			t.Errorf("GET /admin/ from %s: %d", peer, rec.Code)
		}
	}
	if rec := post(h, "203.0.113.5:4000", `{"code":"K7QM-2XRP","url":"http://fresh:8096"}`); rec.Code != http.StatusNotFound {
		t.Errorf("from the internet: %d", rec.Code)
	}

	// A wrong code, a server that doesn't answer, one that fails: nothing
	// is saved, and only plain problems are shown as they are.
	if rec := post(h, local, `{"code":"AAAA-AAAA","url":"http://fresh:8096"}`); rec.Code != http.StatusForbidden {
		t.Errorf("wrong code: %d %s", rec.Code, rec.Body)
	}
	rec := post(h, local, `{"code":"K7QM-2XRP","url":"http://down:8096"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "can't be reached") {
		t.Errorf("down: %d %s", rec.Code, rec.Body)
	}
	rec = post(h, local, `{"code":"K7QM-2XRP","url":"http://broken:8096"}`)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "disk") {
		t.Errorf("broken: %d %s", rec.Code, rec.Body)
	}
	if len(*saved) != 0 {
		t.Fatalf("saved too early: %v", *saved)
	}

	// The code may be typed in lowercase, without its dash.
	rec = post(h, local, `{"code":" k7qm2xrp ","url":"http://fresh:8096"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Jellyfin 12.2.0") {
		t.Fatalf("good setup: %d %s", rec.Code, rec.Body)
	}
	if len(*saved) != 1 || (*saved)[0] != "http://fresh:8096" {
		t.Errorf("saved: %v", *saved)
	}
	// Once set up, the page saves nothing more.
	if rec := post(h, local, `{"code":"K7QM-2XRP","url":"http://fresh2:8096"}`); rec.Code != http.StatusConflict || len(*saved) != 1 {
		t.Errorf("after setup: %d, saved %v", rec.Code, *saved)
	}
}

// A server someone else set up: its administrator's login first, then
// what it holds, and it's handed over only once that very server is
// confirmed.
func TestSetupHandOver(t *testing.T) {
	s, saved := newSetup()
	h := s.Handler()
	const local = "127.0.0.1:50000"
	const other = `{"code":"K7QM-2XRP","url":"http://other:8096"`
	for _, st := range []struct {
		body, want string
		status     int
	}{
		{other + `}`, `"needsLogin":true`, http.StatusOK},
		{other + `,"jellyfinUsername":"admin","jellyfinPassword":"no"}`, "refuses", http.StatusUnprocessableEntity},
		{other + `,"jellyfinUsername":"admin","jellyfinPassword":"pw"}`, `"libraries":3`, http.StatusOK},
		{other + `,"jellyfinUsername":"admin","jellyfinPassword":"pw","confirm":"srv-1"}`, `"libraries":3`, http.StatusOK},
	} {
		if rec := post(h, local, st.body); rec.Code != st.status || !strings.Contains(rec.Body.String(), st.want) {
			t.Errorf("%s: %d %s", st.body, rec.Code, rec.Body)
		}
	}
	if len(*saved) != 0 {
		t.Fatalf("handed over before it was confirmed: %v", *saved)
	}
	rec := post(h, local, other+`,"jellyfinUsername":"admin","jellyfinPassword":"pw","confirm":"srv-2"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "summary") || len(*saved) != 1 {
		t.Errorf("confirmed: %d %s, saved %v", rec.Code, rec.Body, *saved)
	}
}

func TestSetupStopsAfterWrongCodes(t *testing.T) {
	s, saved := newSetup()
	h := s.Handler()
	for range setupAttempts {
		post(h, "127.0.0.1:1", `{"code":"AAAA-AAAA","url":"http://fresh:8096"}`)
	}
	// Even the right code is refused now, until a restart.
	if rec := post(h, "127.0.0.1:1", `{"code":"K7QM-2XRP","url":"http://fresh:8096"}`); rec.Code != http.StatusTooManyRequests || len(*saved) != 0 {
		t.Errorf("after %d wrong codes: %d, saved %v", setupAttempts, rec.Code, *saved)
	}
}

func TestSetupCode(t *testing.T) {
	for range 100 {
		c := NewSetupCode()
		if len(c) != 9 || c[4] != '-' || strings.ContainsAny(c, "01IOL") {
			t.Fatalf("code %q", c)
		}
	}
}

// With managed sign-in, the page is on the site's own address too, and
// also makes the first admin; what's saved stays saved when the rest
// fails.
func TestSetupFirstAdmin(t *testing.T) {
	s, saved := newSetup()
	var admins []string
	fail := true
	s.FirstAdmin = func(_ context.Context, email, hash string) error {
		if fail {
			return errors.New("disk full")
		}
		admins = append(admins, email+" "+hash[:13])
		return nil
	}
	done := 0
	s.Done = func() { done++ }
	h := s.PublicHandler()
	const internet = "203.0.113.5:4000"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/api/setup", nil))
	if rec.Body.String() != `{"admin":true,"fixedUrl":"","jellyfin":true,"site":true}`+"\n" {
		t.Errorf("what it asks: %s", rec.Body)
	}
	for body, want := range map[string]string{
		`{"code":"K7QM-2XRP","url":"http://fresh:8096","email":"not an email","password":"long enough pw"}`: "isn't an email",
		`{"code":"K7QM-2XRP","url":"http://fresh:8096","email":"ann@example.org","password":"short"}`:       "10 to 256",
	} {
		if rec := post(h, internet, body); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if len(*saved) != 0 {
		t.Fatalf("saved before the admin was checked: %v", *saved)
	}
	good := `{"code":"K7QM-2XRP","url":"http://fresh:8096","email":"ann@example.org","password":"long enough pw"}`
	if rec := post(h, internet, good); rec.Code != http.StatusInternalServerError || len(*saved) != 1 || done != 0 {
		t.Fatalf("admin failing: %d, saved %v", rec.Code, *saved)
	}
	// Jellyfin is kept; the retry asks only for the admin.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/api/setup", nil))
	if !strings.Contains(rec.Body.String(), `"jellyfin":false`) {
		t.Errorf("after Jellyfin was saved: %s", rec.Body)
	}
	fail = false
	if rec := post(h, internet, `{"code":"K7QM-2XRP","email":"ann@example.org","password":"long enough pw"}`); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if len(admins) != 1 || admins[0] != "ann@example.org pbkdf2-sha256" || len(*saved) != 1 || done != 1 {
		t.Errorf("admins %v, saved %v, done %d", admins, *saved, done)
	}
}

// On a fresh server, the page asks the default language too.
func TestSetupLanguage(t *testing.T) {
	s, saved := newSetup()
	var language string
	s.Language = func(_ context.Context, tag string) error { language = tag; return nil }
	h := s.Handler()
	const local = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/api/setup", nil)
	req.RemoteAddr = local
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"tag":"fr-CA"`) {
		t.Errorf("what it asks: %s", rec.Body)
	}
	if rec := post(h, local, `{"code":"K7QM-2XRP","url":"http://fresh:8096","language":"xx-YY"}`); rec.Code != 422 || len(*saved) != 0 {
		t.Errorf("no language: %d, saved %v", rec.Code, *saved)
	}
	if rec := post(h, local, `{"code":"K7QM-2XRP","url":"http://fresh:8096","language":"fr-CA"}`); rec.Code != 200 || language != "fr-CA" {
		t.Errorf("with one: %d %s, language %q", rec.Code, rec.Body, language)
	}
}
