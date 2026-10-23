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

	"github.com/overfin/server/internal/introskipper"
	"github.com/overfin/server/internal/jellyfin"
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

func post(h http.Handler, peer, step, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/admin/api/setup/"+step, strings.NewReader(body))
	req.RemoteAddr = peer
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(h http.Handler, peer string) string {
	req := httptest.NewRequest("GET", "/admin/api/setup", nil)
	req.RemoteAddr = peer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}

const local = "127.0.0.1:50000"

func TestSetup(t *testing.T) {
	s, saved := newSetup()
	done := 0
	s.Done = func() { done++ }
	h := s.Handler()

	// The page itself, on a private address only.
	for peer, want := range map[string]int{local: http.StatusOK, "203.0.113.5:4000": http.StatusNotFound} {
		req := httptest.NewRequest("GET", "/admin/", nil)
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("GET /admin/ from %s: %d", peer, rec.Code)
		}
	}
	if rec := post(h, "203.0.113.5:4000", "jellyfin", `{"code":"K7QM-2XRP","url":"http://fresh:8096"}`); rec.Code != http.StatusNotFound {
		t.Errorf("from the internet: %d", rec.Code)
	}
	if got := get(h, local); !strings.Contains(got, `"steps":[{"id":"code","done":false},{"id":"jellyfin","done":false},{"id":"done","done":false}]`) {
		t.Errorf("steps: %s", got)
	}

	// The code alone, checked: lowercase, without its dash too.
	if rec := post(h, local, "code", `{"code":"AAAA-AAAA"}`); rec.Code != http.StatusForbidden {
		t.Errorf("wrong code: %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, local, "code", `{"code":" k7qm2xrp "}`); rec.Code != http.StatusNoContent {
		t.Errorf("right code: %d %s", rec.Code, rec.Body)
	}
	// Each step needs it too.
	if rec := post(h, local, "jellyfin", `{"url":"http://fresh:8096"}`); rec.Code != http.StatusForbidden {
		t.Errorf("no code: %d", rec.Code)
	}

	// A server that doesn't answer, one that fails: nothing is saved, and
	// only plain problems are shown as they are.
	rec := post(h, local, "jellyfin", `{"code":"K7QM-2XRP","url":"http://down:8096"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "can't be reached") {
		t.Errorf("down: %d %s", rec.Code, rec.Body)
	}
	rec = post(h, local, "jellyfin", `{"code":"K7QM-2XRP","url":"http://broken:8096"}`)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "disk") {
		t.Errorf("broken: %d %s", rec.Code, rec.Body)
	}
	// Not finished before Jellyfin is.
	if rec := post(h, local, "finish", `{"code":"K7QM-2XRP"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"step":"jellyfin"`) || done != 0 {
		t.Errorf("finished early: %d %s", rec.Code, rec.Body)
	}
	if len(*saved) != 0 {
		t.Fatalf("saved too early: %v", *saved)
	}

	rec = post(h, local, "jellyfin", `{"code":"K7QM-2XRP","url":"http://fresh:8096"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Jellyfin 12.2.0") {
		t.Fatalf("good Jellyfin: %d %s", rec.Code, rec.Body)
	}
	if len(*saved) != 1 || (*saved)[0] != "http://fresh:8096" {
		t.Errorf("saved: %v", *saved)
	}
	// Done, it stays in the steps; it isn't set up twice.
	if got := get(h, local); !strings.Contains(got, `{"id":"jellyfin","done":true}`) {
		t.Errorf("steps after Jellyfin: %s", got)
	}
	if rec := post(h, local, "jellyfin", `{"code":"K7QM-2XRP","url":"http://fresh2:8096"}`); rec.Code != http.StatusConflict || len(*saved) != 1 {
		t.Errorf("twice: %d, saved %v", rec.Code, *saved)
	}
	if rec := post(h, local, "finish", `{"code":"K7QM-2XRP"}`); rec.Code != http.StatusNoContent || done != 1 {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	// Once set up, the page saves nothing more.
	if rec := post(h, local, "code", `{"code":"K7QM-2XRP"}`); rec.Code != http.StatusConflict {
		t.Errorf("after setup: %d", rec.Code)
	}
}

// A server someone else set up: its administrator's login first, then
// what it holds, and it's handed over only once that very server is
// confirmed.
func TestSetupHandOver(t *testing.T) {
	s, saved := newSetup()
	h := s.Handler()
	const other = `{"code":"K7QM-2XRP","url":"http://other:8096"`
	for _, st := range []struct {
		body, want string
		status     int
	}{
		{other + `}`, `"needsLogin":true`, http.StatusOK},
		{other + `,"username":"admin","password":"no"}`, "refuses", http.StatusUnprocessableEntity},
		{other + `,"username":"admin","password":"pw"}`, `"libraries":3`, http.StatusOK},
		{other + `,"username":"admin","password":"pw","confirm":"srv-1"}`, `"libraries":3`, http.StatusOK},
	} {
		if rec := post(h, local, "jellyfin", st.body); rec.Code != st.status || !strings.Contains(rec.Body.String(), st.want) {
			t.Errorf("%s: %d %s", st.body, rec.Code, rec.Body)
		}
	}
	if len(*saved) != 0 {
		t.Fatalf("handed over before it was confirmed: %v", *saved)
	}
	rec := post(h, local, "jellyfin", other+`,"username":"admin","password":"pw","confirm":"srv-2"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"done":true`) || len(*saved) != 1 {
		t.Errorf("confirmed: %d %s, saved %v", rec.Code, rec.Body, *saved)
	}
}

func TestSetupStopsAfterWrongCodes(t *testing.T) {
	s, saved := newSetup()
	h := s.Handler()
	for range setupAttempts {
		post(h, "127.0.0.1:1", "code", `{"code":"AAAA-AAAA"}`)
	}
	// Even the right code is refused now, until a restart.
	if rec := post(h, "127.0.0.1:1", "jellyfin", `{"code":"K7QM-2XRP","url":"http://fresh:8096"}`); rec.Code != http.StatusTooManyRequests || len(*saved) != 0 {
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
// also makes the first admin.
func TestSetupFirstAdmin(t *testing.T) {
	s, _ := newSetup()
	var admins []string
	fail := true
	s.FirstAdmin = func(_ context.Context, email, hash string) error {
		if fail {
			return errors.New("disk full")
		}
		admins = append(admins, email+" "+hash[:13])
		return nil
	}
	h := s.PublicHandler()
	const internet = "203.0.113.5:4000"

	if got := get(h, internet); !strings.Contains(got, `{"id":"admin","done":false}`) || !strings.Contains(got, `"site":true`) {
		t.Errorf("what it asks: %s", got)
	}
	for body, want := range map[string]string{
		`{"code":"K7QM-2XRP","email":"not an email","password":"long enough pw"}`: "isn't an email",
		`{"code":"K7QM-2XRP","email":"ann@example.org","password":"short"}`:       "10 to 256",
	} {
		if rec := post(h, internet, "admin", body); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	good := `{"code":"K7QM-2XRP","email":"ann@example.org","password":"long enough pw"}`
	if rec := post(h, internet, "admin", good); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "disk") {
		t.Fatalf("admin failing: %d %s", rec.Code, rec.Body)
	}
	fail = false
	if rec := post(h, internet, "admin", good); rec.Code != http.StatusNoContent {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if len(admins) != 1 || admins[0] != "ann@example.org pbkdf2-sha256" {
		t.Errorf("admins %v", admins)
	}
	if rec := post(h, internet, "admin", good); rec.Code != http.StatusConflict || len(admins) != 1 {
		t.Errorf("twice: %d, admins %v", rec.Code, admins)
	}
}

// On a fresh server, the wizard asks the default language too.
func TestSetupLanguage(t *testing.T) {
	s, _ := newSetup()
	var language string
	s.Language = func(_ context.Context, tag string) error { language = tag; return nil }
	h := s.Handler()
	if got := get(h, local); !strings.Contains(got, `"tag":"fr-CA"`) || !strings.Contains(got, `{"id":"language","done":false}`) {
		t.Errorf("what it asks: %s", got)
	}
	if rec := post(h, local, "language", `{"code":"K7QM-2XRP","language":"xx-YY"}`); rec.Code != 422 || language != "" {
		t.Errorf("no language: %d, language %q", rec.Code, language)
	}
	if rec := post(h, local, "language", `{"code":"K7QM-2XRP","language":"fr-CA"}`); rec.Code != 204 || language != "fr-CA" {
		t.Errorf("with one: %d %s, language %q", rec.Code, rec.Body, language)
	}
}

// TMDB's token may be given, tested by TMDB, or skipped.
func TestSetupTMDB(t *testing.T) {
	s, _ := newSetup()
	var key string
	s.TMDB = func(_ context.Context, k string) (string, error) {
		if k != "good" {
			return "", Problem("TMDB refuses that token.")
		}
		key = k
		return "TMDB takes the token.", nil
	}
	done := 0
	s.Done = func() { done++ }
	h := s.Handler()
	if got := get(h, local); !strings.Contains(got, `{"id":"tmdb","done":false,"optional":true}`) {
		t.Errorf("what it asks: %s", got)
	}
	for body, want := range map[string]string{`{"code":"K7QM-2XRP","token":" "}`: "Paste", `{"code":"K7QM-2XRP","token":"bad"}`: "refuses"} {
		if rec := post(h, local, "tmdb", body); rec.Code != 422 || !strings.Contains(rec.Body.String(), want) || key != "" {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := post(h, local, "tmdb", `{"code":"K7QM-2XRP","token":"good"}`); rec.Code != 200 || key != "good" {
		t.Errorf("good: %d %s", rec.Code, rec.Body)
	}
	// Skipped or not, it doesn't hold the wizard back.
	post(h, local, "jellyfin", `{"code":"K7QM-2XRP","url":"http://fresh:8096"}`)
	if rec := post(h, local, "finish", `{"code":"K7QM-2XRP"}`); rec.Code != 204 || done != 1 {
		t.Errorf("finish: %d %s", rec.Code, rec.Body)
	}
}

// Intro Skipper's step: optional, and only once Overfin has a Jellyfin.
func TestSetupIntroSkipper(t *testing.T) {
	s, _ := newSetup()
	s.IntroSkipper = &introskipper.Installer{Logger: s.Logger,
		Jellyfin: func() (*jellyfin.Client, jellyfin.Auth, bool) { return nil, jellyfin.Auth{}, false }}
	h := s.Handler()
	if got := get(h, local); !strings.Contains(got, `{"id":"introskipper","done":false,"optional":true}`) {
		t.Errorf("what it asks: %s", got)
	}
	if rec := post(h, local, "introskipper", `{"code":"K7QM-2XRP","start":true}`); rec.Code != 422 || !strings.Contains(rec.Body.String(), "no Jellyfin") {
		t.Errorf("without Jellyfin: %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, local, "introskipper", `{"code":"K7QM-2XRP"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"running":false`) {
		t.Errorf("where it stands: %d %s", rec.Code, rec.Body)
	}
}

// Seerr's step: optional, its address and key both needed.
func TestSetupSeerr(t *testing.T) {
	s, _ := newSetup()
	var got string
	s.Seerr = func(_ context.Context, url, key string) (string, error) {
		if key != "good" {
			return "", Problem("Seerr refuses this API key.")
		}
		got = url
		return "Seerr's API key works. No default Sonarr is connected in Seerr.", nil
	}
	h := s.Handler()
	if body := get(h, local); !strings.Contains(body, `{"id":"seerr","done":false,"optional":true}`) {
		t.Errorf("what it asks: %s", body)
	}
	for body, want := range map[string]string{
		`{"code":"K7QM-2XRP","url":"http://seerr:5055","key":""}`:    "Give Seerr's address",
		`{"code":"K7QM-2XRP","url":"http://seerr:5055","key":"bad"}`: "refuses",
	} {
		if rec := post(h, local, "seerr", body); rec.Code != 422 || !strings.Contains(rec.Body.String(), want) || got != "" {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec := post(h, local, "seerr", `{"code":"K7QM-2XRP","url":"http://seerr:5055","key":"good"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No default Sonarr") || got != "http://seerr:5055" {
		t.Errorf("good: %d %s", rec.Code, rec.Body)
	}
}
