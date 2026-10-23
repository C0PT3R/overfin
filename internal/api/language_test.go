package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/overfin/server/internal/store"
)

func TestAccountLanguage(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	if _, err := st.SetLanguages(context.Background(), []string{"fr-CA", "en-US"}, "fr-CA"); err != nil {
		t.Fatal(err)
	}
	do := func(who, method, path, body, accept string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Remote-User", who)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if accept != "" {
			req.Header.Set("Accept-Language", accept)
		}
		res, err := client(srv, who).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// A new account takes the browser's first language among those offered.
	if code, body := do("anglo", "POST", "/api/v1/me", "{}", "de-DE, en-GB;q=0.8"); code != 201 || !strings.Contains(body, `"language":"en-US"`) ||
		!strings.Contains(body, `"tag":"fr-CA"`) {
		t.Errorf("anglo: %d %s", code, body)
	}
	// None offered: the default. And once chosen, the browser no longer
	// matters.
	if _, body := do("other", "POST", "/api/v1/me", "{}", "de-DE"); !strings.Contains(body, `"language":"fr-CA"`) {
		t.Errorf("other: %s", body)
	}
	if _, body := do("anglo", "GET", "/api/v1/me", "", "fr-CA"); !strings.Contains(body, `"language":"en-US"`) {
		t.Errorf("anglo again: %s", body)
	}

	// The owner changes it, to one offered.
	if code, _ := do("anglo", "PATCH", "/api/v1/me", `{"language":"de-DE"}`, ""); code != 400 {
		t.Errorf("not offered: %d", code)
	}
	if code, body := do("anglo", "PATCH", "/api/v1/me", `{"language":"fr-CA"}`, ""); code != 200 || !strings.Contains(body, `"language":"fr-CA"`) {
		t.Errorf("changed: %d %s", code, body)
	}
	// Removed by the operator: the default applies.
	if _, err := st.SetLanguages(context.Background(), []string{"en-US"}, "en-US"); err != nil {
		t.Fatal(err)
	}
	if _, body := do("anglo", "GET", "/api/v1/me", "", ""); !strings.Contains(body, `"language":"en-US"`) {
		t.Errorf("no longer offered: %s", body)
	}
}
