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

func newSetup() (*Setup, *[]string) {
	var saved []string
	s := &Setup{Code: "K7QM-2XRP", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Test: func(_ context.Context, url, key string) (string, error) {
			if key != "good" {
				return "", errors.New("Jellyfin refuses its API key.")
			}
			return "Jellyfin 12.2.0, at " + url, nil
		},
		Save: func(_ context.Context, url, key string) error {
			saved = append(saved, url+" "+key)
			return nil
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
	if rec := post(h, "203.0.113.5:4000", `{"code":"K7QM-2XRP","url":"http://192.0.2.10:8096","key":"good"}`); rec.Code != http.StatusNotFound {
		t.Errorf("from the internet: %d", rec.Code)
	}

	// A wrong code, then a failing test: nothing is saved.
	if rec := post(h, local, `{"code":"AAAA-AAAA","url":"http://192.0.2.10:8096","key":"good"}`); rec.Code != http.StatusForbidden {
		t.Errorf("wrong code: %d %s", rec.Code, rec.Body)
	}
	rec := post(h, local, `{"code":"K7QM-2XRP","url":"http://192.0.2.10:8096","key":"bad"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "refuses its API key") {
		t.Errorf("failing test: %d %s", rec.Code, rec.Body)
	}
	if len(*saved) != 0 {
		t.Fatalf("saved too early: %v", *saved)
	}

	// The code may be typed in lowercase, without its dash; the address
	// loses its trailing slash.
	rec = post(h, local, `{"code":" k7qm2xrp ","url":"http://192.0.2.10:8096/","key":"good"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Jellyfin 12.2.0") {
		t.Fatalf("good setup: %d %s", rec.Code, rec.Body)
	}
	if len(*saved) != 1 || (*saved)[0] != "http://192.0.2.10:8096 good" {
		t.Errorf("saved: %v", *saved)
	}
	// Once set up, the page saves nothing more.
	if rec := post(h, local, `{"code":"K7QM-2XRP","url":"http://192.0.2.99:8096","key":"good"}`); rec.Code != http.StatusConflict || len(*saved) != 1 {
		t.Errorf("after setup: %d, saved %v", rec.Code, *saved)
	}
}

func TestSetupStopsAfterWrongCodes(t *testing.T) {
	s, saved := newSetup()
	h := s.Handler()
	for range setupAttempts {
		post(h, "127.0.0.1:1", `{"code":"AAAA-AAAA","url":"http://192.0.2.10:8096","key":"good"}`)
	}
	// Even the right code is refused now, until a restart.
	if rec := post(h, "127.0.0.1:1", `{"code":"K7QM-2XRP","url":"http://192.0.2.10:8096","key":"good"}`); rec.Code != http.StatusTooManyRequests || len(*saved) != 0 {
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
	if rec.Body.String() != `{"admin":true,"jellyfin":true,"site":true}`+"\n" {
		t.Errorf("what it asks: %s", rec.Body)
	}
	for body, want := range map[string]string{
		`{"code":"K7QM-2XRP","url":"http://192.0.2.10:8096","key":"good","email":"not an email","password":"long enough pw"}`: "isn't an email",
		`{"code":"K7QM-2XRP","url":"http://192.0.2.10:8096","key":"good","email":"ann@example.org","password":"short"}`:       "10 to 256",
	} {
		if rec := post(h, internet, body); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if len(*saved) != 0 {
		t.Fatalf("saved before the admin was checked: %v", *saved)
	}
	good := `{"code":"K7QM-2XRP","url":"http://192.0.2.10:8096","key":"good","email":"ann@example.org","password":"long enough pw"}`
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
