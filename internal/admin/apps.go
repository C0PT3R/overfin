package admin

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/overfin/server/internal/store"
)

// Apps: keys the operator gives other apps (Stewarr…), and what they may
// read with them, on this private listener (SECURITY.md rule 58): totals
// per title, never anything per person (rule 40).

// appScopes are what a key may be for.
var appScopes = map[string]bool{"watch-stats": true}

type appKeyJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	CreatedAt string `json:"createdAt"`
	LastUsed  string `json:"lastUsedAt,omitempty"`
	// Key is the key itself, only in the answer that creates it.
	Key string `json:"key,omitempty"`
}

func appKeyOf(k store.AppKey) appKeyJSON {
	j := appKeyJSON{ID: k.ID, Name: k.Name, Scope: k.Scope, CreatedAt: k.Created.Format(time.RFC3339)}
	if !k.LastUsed.IsZero() {
		j.LastUsed = k.LastUsed.Format(time.RFC3339)
	}
	return j
}

func (p *Panel) appKeys(w http.ResponseWriter, r *http.Request, _ *session) {
	keys, err := p.Store.AppKeys(r.Context())
	if err != nil {
		p.fail(w, "listing app keys", err)
		return
	}
	out := []appKeyJSON{}
	for _, k := range keys {
		out = append(out, appKeyOf(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (p *Panel) createAppKey(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || !appScopes[req.Scope] || strings.TrimSpace(req.Name) == "" ||
		utf8.RuneCountInString(req.Name) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "an app needs a name and what it's for"})
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	key := "ofapp_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	k, err := p.Store.CreateAppKey(r.Context(), strings.TrimSpace(req.Name), req.Scope, key, p.clock())
	if err != nil {
		p.fail(w, "creating an app key", err)
		return
	}
	p.Logger.Info("an app key was created in the control panel", "app", k.ID, "scope", k.Scope, "by", s.name)
	j := appKeyOf(*k)
	j.Key = key
	writeJSON(w, http.StatusCreated, j)
}

func (p *Panel) deleteAppKey(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	switch err := p.Store.DeleteAppKey(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"type": "not-found"})
		return
	case err != nil:
		p.fail(w, "revoking an app key", err)
		return
	}
	p.Logger.Info("an app key was revoked in the control panel", "app", id, "by", s.name)
	w.WriteHeader(http.StatusNoContent)
}

// appKey is the key a request presents (Authorization: Bearer …), when it
// may do scope; otherwise it answers 401 and returns nil.
func (p *Panel) appKey(w http.ResponseWriter, r *http.Request, scope string) *store.AppKey {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(token, "ofapp_") || len(token) > 100 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"type": "not-authenticated"})
		return nil
	}
	k, err := p.Store.AppKeyFor(r.Context(), token, p.clock())
	if errors.Is(err, store.ErrNotFound) || (err == nil && k.Scope != scope) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"type": "not-authenticated"})
		return nil
	}
	if err != nil {
		p.fail(w, "checking an app key", err)
		return nil
	}
	return k
}

type watchStatsJSON struct {
	Kind       string `json:"kind"`
	TMDB       string `json:"tmdbId,omitempty"`
	IMDb       string `json:"imdbId,omitempty"`
	TVDB       string `json:"tvdbId,omitempty"`
	Plays      int    `json:"plays"`
	Viewers    int    `json:"viewers"`
	Favorites  int    `json:"favorites"`
	LastPlayed string `json:"lastPlayed,omitempty"` // YYYY-MM-DD
	Changed    string `json:"changedAt"`
}

// watchStats (GET /apps/v1/watch-stats?since=RFC 3339) answers, for each
// film and series anyone watched or keeps as a favorite, what the profiles
// did with it in total: plays to the end, viewers (accounts), favorites
// (profiles), the day it was last played. Never anything per person.
func (p *Panel) watchStats(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "watch-stats") == nil {
		return
	}
	var since time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		var err error
		if since, err = time.Parse(time.RFC3339, s); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "since: an RFC 3339 time"})
			return
		}
	}
	now := p.clock()
	all, err := p.Store.AllWatchStats(r.Context(), since)
	if err != nil {
		p.fail(w, "reading watch statistics", err)
		return
	}
	out := make([]watchStatsJSON, 0, len(all))
	for _, s := range all {
		j := watchStatsJSON{Kind: s.Kind, TMDB: s.TMDB, IMDb: s.IMDb, TVDB: s.TVDB, Plays: s.Plays, Viewers: s.Viewers,
			Favorites: s.Favorites, Changed: s.Changed.Format(time.RFC3339)}
		if !s.LastPlayed.IsZero() {
			j.LastPlayed = s.LastPlayed.Format(time.DateOnly)
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"titles": out, "generatedAt": now.UTC().Format(time.RFC3339)})
}
