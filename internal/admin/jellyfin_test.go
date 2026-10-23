package admin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// memJellyfin is Overfin's Jellyfin as the panel sees it.
type memJellyfin struct {
	open  bool
	since time.Time
}

func (m *memJellyfin) Jellyfin() (string, bool) { return "http://jellyfin:8096", true }

func (m *memJellyfin) SetJellyfin(context.Context, string) (string, error) {
	return "", Problem("Jellyfin is set in Overfin's configuration file: change it there.")
}

func (m *memJellyfin) JellyfinAccess(context.Context) (bool, time.Time, error) {
	return m.open, m.since, nil
}

func (m *memJellyfin) SetJellyfinAccess(_ context.Context, open bool) (string, string, error) {
	m.open, m.since = open, time.Now()
	if open {
		return "overfin-admin", "fresh-password", nil
	}
	return "", "", nil
}

func TestJellyfinAccess(t *testing.T) {
	p, idp, _ := newPanel(t)
	jf := &memJellyfin{}
	p.Jellyfin = jf
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	if rec := b.do("PUT", "/admin/api/jellyfin/access", `{"open":true}`, nil); rec.Code != 401 || jf.open {
		t.Fatalf("before signing in: %d", rec.Code)
	}
	signIn(t, b, idp, []string{"overfin-admins"})

	if rec := b.do("GET", "/admin/api/jellyfin", "", nil); !strings.Contains(rec.Body.String(), `"access":{"open":false}`) {
		t.Errorf("closed: %s", rec.Body)
	}
	rec := b.do("PUT", "/admin/api/jellyfin/access", `{"open":true}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"password":"fresh-password"`) || !jf.open {
		t.Errorf("open: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("GET", "/admin/api/jellyfin", "", nil); !strings.Contains(rec.Body.String(), `"open":true,"since"`) ||
		strings.Contains(rec.Body.String(), "fresh-password") {
		t.Errorf("open, later: %s", rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/jellyfin/access", `{"open":false}`, nil); rec.Code != 204 || jf.open {
		t.Errorf("close: %d %s", rec.Code, rec.Body)
	}
}

// memTMDB is the operator's TMDB token as the panel sees it.
type memTMDB struct{ token string }

func (m *memTMDB) TMDBToken() (bool, bool) { return m.token != "", false }

func (m *memTMDB) SetTMDBToken(_ context.Context, token string) (string, error) {
	if token == "bad" {
		return "", Problem("TMDB refuses that token.")
	}
	m.token = token
	return "ok", nil
}

func TestTMDBToken(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	k := &memTMDB{}
	p.TMDBTokens = k
	if rec := b.do("PUT", "/admin/api/tmdb-token", `{"token":"bad"}`, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "refuses") {
		t.Errorf("refused: %d %s", rec.Code, rec.Body)
	}
	rec := b.do("PUT", "/admin/api/tmdb-token", `{"token":"good"}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"token":{"set":true,"inEnv":false}`) || k.token != "good" {
		t.Errorf("set: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("GET", "/admin/api/ratings", "", nil); strings.Contains(rec.Body.String(), "good") {
		t.Errorf("the token is shown: %s", rec.Body)
	}
}
