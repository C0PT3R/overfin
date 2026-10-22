package cookie

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSchemes(t *testing.T) {
	plain := httptest.NewRequest("GET", "http://192.0.2.10:8080/", nil)
	tlsReq := httptest.NewRequest("GET", "https://example.org/", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	proxied := httptest.NewRequest("GET", "http://example.org/", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")

	for name, c := range map[string]struct {
		r          *http.Request
		wantName   string
		wantSecure bool
	}{
		"plain http":  {plain, "overfin", false},
		"tls":         {tlsReq, "__Host-overfin", true},
		"https proxy": {proxied, "__Host-overfin", true},
	} {
		rec := httptest.NewRecorder()
		Set(rec, c.r, &http.Cookie{Name: "overfin", Value: "v", Path: "/elsewhere", SameSite: http.SameSiteNoneMode})
		got := rec.Result().Cookies()[0]
		if got.Name != c.wantName || got.Secure != c.wantSecure || !got.HttpOnly || got.Path != "/" ||
			got.SameSite != http.SameSiteStrictMode {
			t.Errorf("%s: %+v", name, got)
		}
	}

	// Marked always secure: HTTPS names even on a plain connection.
	var seen bool
	AlwaysSecure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = Secure(r) })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.org/", nil))
	if !seen {
		t.Error("AlwaysSecure didn't make the request secure")
	}

	// A plain-http cookie isn't read over HTTPS, nor the other way round.
	tlsReq.AddCookie(&http.Cookie{Name: "overfin", Value: "planted"})
	if _, err := Get(tlsReq, "overfin"); err == nil {
		t.Error("a plain cookie was read over HTTPS")
	}
	plain.AddCookie(&http.Cookie{Name: "__Host-overfin", Value: "v"})
	if _, err := Get(plain, "overfin"); err == nil {
		t.Error("an HTTPS cookie was read over plain http")
	}
}
