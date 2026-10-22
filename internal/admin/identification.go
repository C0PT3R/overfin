package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/overfin/server/internal/identify"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// Identification: every film and series of the libraries with the TMDB
// title it's taken for, those that may be wrong first (identify), and the
// admin's fix: another TMDB title, kept by Overfin (store.SetOverride).
// Confirming a suspect is choosing the title it already has.

type identifiedJSON struct {
	ItemID       string            `json:"itemId"`
	Kind         string            `json:"kind"`
	LibraryID    string            `json:"libraryId,omitempty"`
	Name         string            `json:"name"`
	FolderName   string            `json:"folderName,omitempty"`
	FileName     string            `json:"fileName,omitempty"`
	RuntimeMin   int               `json:"runtimeMin,omitempty"`
	Episodes     int               `json:"episodes,omitempty"`
	TMDB         string            `json:"tmdb,omitempty"`
	JellyfinTMDB string            `json:"jellyfinTmdb,omitempty"`
	Overridden   bool              `json:"overridden"`
	TMDBName     string            `json:"tmdbName,omitempty"`
	TMDBYear     int               `json:"tmdbYear,omitempty"`
	OriginalName string            `json:"originalName,omitempty"`
	Language     string            `json:"language,omitempty"`
	Reasons      []identify.Reason `json:"reasons"`
}

func (p *Panel) identification(w http.ResponseWriter, r *http.Request, _ *session) {
	all, err := p.Store.LibraryTitles(r.Context())
	if err != nil {
		p.fail(w, "listing the library's titles", err)
		return
	}
	out := make([]identifiedJSON, 0, len(all))
	suspects := 0
	for _, t := range all {
		j := identifiedJSON{ItemID: t.ItemID, Kind: t.Kind, LibraryID: t.LibraryID, Name: t.Name,
			FolderName: t.FolderName, FileName: t.FileName, RuntimeMin: int(t.RuntimeMs / 60_000),
			TMDB: t.TMDB, JellyfinTMDB: t.JellyfinTMDB, Overridden: t.Overridden,
			TMDBName: t.TMDBName, TMDBYear: t.TMDBYear, OriginalName: t.Details.OriginalName, Language: t.Details.Language,
			Reasons: identify.Reasons(t)}
		if j.Reasons == nil {
			j.Reasons = []identify.Reason{}
		} else {
			suspects++
		}
		for _, n := range t.Episodes {
			j.Episodes += n
		}
		out = append(out, j)
	}
	// Suspects first, those with the most reasons first; the rest keep
	// their order (by name).
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Reasons) > len(out[j].Reasons) })
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "suspects": suspects, "tmdb": p.SearchTMDB != nil})
}

type candidateJSON struct {
	TMDB         string `json:"tmdb"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Year         int    `json:"year,omitempty"`
	OriginalName string `json:"originalName,omitempty"`
	Language     string `json:"language,omitempty"`
	Overview     string `json:"overview,omitempty"`
	Poster       string `json:"poster,omitempty"` // a panel image URL
}

// identificationSearch searches TMDB for the title a film or a series
// really is. The words travel in the body (SECURITY.md rule 48).
func (p *Panel) identificationSearch(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || (req.Kind != "movie" && req.Kind != "series") ||
		utf8.RuneCountInString(req.Query) > 200 || strings.TrimSpace(req.Query) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "a search needs words and a kind"})
		return
	}
	if p.SearchTMDB == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "no-tmdb", "detail": "Overfin has no TMDB to search"})
		return
	}
	found, err := p.SearchTMDB(r.Context(), strings.TrimSpace(req.Query))
	if err != nil {
		p.Logger.Warn("searching TMDB from the control panel failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"type": "upstream", "detail": "TMDB couldn't be searched"})
		return
	}
	out := []candidateJSON{}
	for _, t := range found {
		if t.Kind != req.Kind {
			continue
		}
		c := candidateJSON{TMDB: strconv.Itoa(t.TMDBID), Kind: t.Kind, Name: t.Name, Year: t.Year,
			OriginalName: t.OriginalName, Language: t.Language, Overview: t.Overview}
		if id := images.ArtworkID("poster", t.Poster); id != "" {
			c.Poster = "/admin/api/posters/" + id
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// itemIDPattern is a Jellyfin item ID as the index keeps them.
var itemIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// identify makes a film or a series the TMDB title the admin chose; the
// one it already has confirms it.
func (p *Panel) identify(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	var req struct {
		TMDB string `json:"tmdb"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	n, convErr := 0, error(nil)
	if err := dec.Decode(&req); err == nil {
		n, convErr = strconv.Atoi(req.TMDB)
	}
	if !itemIDPattern.MatchString(id) || convErr != nil || !tmdb.ValidID(n) || strconv.Itoa(n) != req.TMDB {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a TMDB ID"})
		return
	}
	p.retitled(w, r, s, id, req.TMDB, p.Store.SetOverride(r.Context(), id, req.TMDB, p.clock()))
}

// unidentify gives a film or a series back the title Jellyfin gave it.
func (p *Panel) unidentify(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	if !itemIDPattern.MatchString(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"type": "not-found"})
		return
	}
	p.retitled(w, r, s, id, "", p.Store.ClearOverride(r.Context(), id))
}

func (p *Panel) retitled(w http.ResponseWriter, r *http.Request, s *session, id, tmdbID string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"type": "not-found"})
		return
	case err != nil:
		p.fail(w, "identifying a title", err)
		return
	}
	if tmdbID != "" {
		p.Logger.Info("a title was identified in the control panel", "item", id, "tmdb", tmdbID, "by", s.name)
	} else {
		p.Logger.Info("a title went back to Jellyfin's identification", "item", id, "by", s.name)
	}
	// The new title's page and artwork are fetched now.
	if p.OnRetitled != nil {
		p.OnRetitled()
	}
	w.WriteHeader(http.StatusNoContent)
}

// poster serves a TMDB poster to the panel (search results), through
// Overfin, as everywhere (SECURITY.md rule 43).
func (p *Panel) poster(w http.ResponseWriter, r *http.Request, _ *session) {
	ref, err := images.ParseArtwork(r.PathValue("id"))
	if err != nil || ref.Type != "poster" || p.Images == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"type": "not-found"})
		return
	}
	data, contentType, err := p.Images.TMDB(r.Context(), ref, "small", func(context.Context) error { return nil })
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"type": "not-found"})
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(data)
}
