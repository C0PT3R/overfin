package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/store"
)

// Library serves browsing: libraries, item queries, details and the home
// rows. Every call to Jellyfin runs as Overfin's one Jellyfin account
// (milestone 4, step 4); what each profile may see is Overfin's to decide
// (access.go), and what it did with titles is Overfin's to keep.
type Library struct {
	Sessions    *Sessions
	Provisioner *provision.Provisioner
	Jellyfin    *jellyfin.Client
	Images      *images.Pipeline
	CursorKey   []byte
	Logger      *slog.Logger
	// Folders remembers which library items the index doesn't have are in,
	// for profiles limited to some libraries.
	Folders *FolderCache
	// Service is Overfin's one Jellyfin account, which serves everyone.
	Service *provision.Service
	// Titles, when set, fetches from TMDB what a title's page needs and
	// the catalog doesn't have yet, and searches it.
	Titles *catalog.Titles
	// Searches, when set, limits how often each account searches.
	Searches *SearchLimiter
	// Requested, when set, is what Seerr says anyone requested.
	Requested *SeerrRequested
}

func (Library) Name() string { return "library" }

func (l Library) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/libraries":   l.libraries,
		"GET /api/v1/genres":      l.genres,
		"GET /api/v1/images/{id}": l.image,
		// Titles, in a library or not (milestone 5).
		"GET /api/v1/titles":              l.titles,
		"GET /api/v1/titles/latest":       l.latestTitles,
		"GET /api/v1/titles/continue":     l.continueTitles,
		"GET /api/v1/titles/{id}":         l.title,
		"GET /api/v1/titles/{id}/up-next": l.upNext,
		"GET /api/v1/people/{id}":         l.person,
		"POST /api/v1/search":             l.search,
		// What the profile does with titles (rule 40).
		"PUT /api/v1/titles/{id}/played":      l.markTitlePlayed,
		"DELETE /api/v1/titles/{id}/played":   l.unmarkTitlePlayed,
		"PUT /api/v1/titles/{id}/favorite":    l.markTitleFavorite,
		"DELETE /api/v1/titles/{id}/favorite": l.unmarkTitleFavorite,
		"DELETE /api/v1/titles/{id}/progress": l.forgetTitleProgress,
		"DELETE /api/v1/me/history":           l.clearHistory,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// indexReady reports whether the catalog's index can answer listings: once
// it has read the whole library at least once.
func (l Library) indexReady(r *http.Request) bool {
	st, err := l.Provisioner.Store.CatalogState(r.Context())
	return err == nil && st.Pass > 0
}

// active returns the caller's active profile, or writes the problem and
// returns nil.
func (l Library) active(w http.ResponseWriter, r *http.Request) *store.Profile {
	if c := l.Sessions.Current(w, r); c != nil {
		return c.Profile
	}
	return nil
}

// account returns Overfin's Jellyfin account (its credentials, on its own
// device, and its user ID), or writes the problem and returns false.
func (l Library) account(w http.ResponseWriter, r *http.Request) (jellyfin.Auth, string, bool) {
	a, uid, err := l.jf(r.Context())
	if err != nil {
		l.upstreamError(w, err)
		return jellyfin.Auth{}, "", false
	}
	return a, uid, true
}

// jf returns Overfin's Jellyfin account.
func (l Library) jf(ctx context.Context) (jellyfin.Auth, string, error) {
	if l.Service == nil {
		return jellyfin.Auth{}, "", errors.New("api: no Jellyfin account")
	}
	return l.Service.Auth(ctx)
}

// upstreamError answers for a failed Jellyfin call: "not found" when Jellyfin
// says so (which also covers items the profile may not see), a bare internal
// error otherwise. Jellyfin's own message never reaches the client.
func (l Library) upstreamError(w http.ResponseWriter, err error) {
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	// Jellyfin refused the account's token: a new one, for what follows.
	if jellyfin.IsStatus(err, http.StatusUnauthorized) && l.Service != nil {
		go func() {
			if err := l.Service.Refused(context.Background()); err != nil {
				l.Logger.Error("renewing the Jellyfin account's token failed", "err", err)
			}
		}()
	}
	l.Logger.Error("jellyfin call failed", "err", err)
	problem.Send(w, problem.Internal, "")
}

// hidden answers for an item the profile may not see, or for the error
// that kept Overfin from knowing.
func (l Library) hidden(w http.ResponseWriter, err error) {
	if err != nil {
		l.storeError(w, err)
		return
	}
	problem.Send(w, problem.NotFound, "")
}

// ---- Endpoints --------------------------------------------------------------

func (l Library) libraries(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	raw, err := l.libraryItems(r.Context())
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	if raw, err = l.keep(r.Context(), p, raw); err != nil {
		l.storeError(w, err)
		return
	}
	out := []library.Library{}
	for i := range raw {
		if lib, ok := library.LibraryOf(&raw[i]); ok {
			out = append(out, lib)
		}
	}
	writeOK(w, map[string]any{"items": out})
}

// libraryItems are the libraries, as the index last read them; from
// Jellyfin itself only before the index's first read.
func (l Library) libraryItems(ctx context.Context) ([]jellyfin.RawItem, error) {
	return libraryItems(ctx, l.Provisioner.Store, l.Service, l.Jellyfin)
}

func libraryItems(ctx context.Context, st *store.Store, service *provision.Service, jf *jellyfin.Client) ([]jellyfin.RawItem, error) {
	kept, err := st.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		a, uid, err := service.Auth(ctx)
		if err != nil {
			return nil, err
		}
		return jf.Libraries(ctx, a, uid)
	}
	out := make([]jellyfin.RawItem, 0, len(kept))
	for _, k := range kept {
		var it jellyfin.RawItem
		if err := json.Unmarshal(k.Item, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// shuffle orders ids by HMAC-SHA256(seed, id). Unlike a seeded random
// generator, this never changes between Go versions, and each item's place
// depends only on the seed and its own ID.
func shuffle(ids []string, seed string) {
	score := make(map[string][]byte, len(ids))
	for _, id := range ids {
		h := hmac.New(sha256.New, []byte(seed))
		h.Write([]byte(id))
		score[id] = h.Sum(nil)
	}
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(score[ids[i]], score[ids[j]]) < 0 })
}

func newSeed() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// genres lists the genres of a library's films and series, from the index,
// counting only what the profile may see; nothing until the index is read.
func (l Library) genres(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "library") {
		return
	}
	var lib string
	if s := r.URL.Query().Get("library"); s != "" {
		var ok bool
		if lib, ok = library.Unwrap(library.LibraryPrefix, s); !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	type genre struct {
		Name      string `json:"name"`
		ItemCount int    `json:"itemCount"`
	}
	out := []genre{}
	if l.indexReady(r) {
		a, err := l.access(r.Context(), p)
		if err != nil {
			l.storeError(w, err)
			return
		}
		all, err := l.Provisioner.Store.CatalogGenres(r.Context(), lib, a)
		if err != nil {
			l.storeError(w, err)
			return
		}
		for _, g := range all {
			out = append(out, genre{g.Name, g.Count})
		}
	}
	writeOK(w, map[string]any{"items": out})
}

// image serves one image, access-checked and re-encoded (rules 10 and 27).
// Anything wrong, including an item the profile may not see, is "not found".
func (l Library) image(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "size") {
		return
	}
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "medium"
	}
	if id := r.PathValue("id"); strings.HasPrefix(id, "tmdbart_") {
		if !slices.Contains(images.Sizes, size) {
			problem.Send(w, problem.NotFound, "")
			return
		}
		if p := l.active(w, r); p != nil {
			l.artworkImage(w, r, p, id, size)
		}
		return
	}
	ref, err := images.Parse(r.PathValue("id"))
	if err != nil || !slices.Contains(images.Sizes, size) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ja, uid, ok := l.account(w, r)
	if !ok {
		return
	}
	v := images.Viewer{ProfileID: p.ID, UserID: uid, Auth: ja}
	if a, err := l.access(r.Context(), p); err != nil {
		l.storeError(w, err)
		return
	} else if a != nil {
		v.May = func(ctx context.Context, it *jellyfin.RawItem) (bool, error) { return l.mayOpen(ctx, p, it) }
	}
	data, contentType, err := l.Images.Image(r.Context(), v, ref, size)
	switch {
	case err == nil:
	case errors.Is(err, images.ErrInvalid), errors.Is(err, images.ErrHidden):
		problem.Send(w, problem.NotFound, "")
		return
	case errors.Is(err, images.ErrUnsafe):
		l.Logger.Warn("refused an unsafe image from upstream", "image", r.PathValue("id"))
		problem.Send(w, problem.NotFound, "")
		return
	default:
		l.upstreamError(w, err)
		return
	}
	// The tag in the URL changes whenever the image does, so browsers may
	// keep it; "private" keeps it out of shared caches, since images are
	// access-checked per user.
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// ---- Paging -----------------------------------------------------------------

// cursorBinding describes the query canonically: route, profile and every
// parameter except the cursor itself, in a fixed order.
func cursorBinding(r *http.Request, profileID string) string {
	q := r.URL.Query()
	q.Del("cursor")
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(r.URL.Path + "\n" + profileID)
	for _, k := range keys {
		vals := slices.Clone(q[k])
		sort.Strings(vals)
		b.WriteString("\n" + k + "=" + strings.Join(vals, "\x1f"))
	}
	return b.String()
}

// ---- Parameter parsing ------------------------------------------------------

// onlyParams refuses any query parameter not in allowed, so a typo never
// silently changes a query.
func onlyParams(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for k := range r.URL.Query() {
		if !slices.Contains(allowed, k) {
			problem.Send(w, problem.InvalidRequest, "unknown parameter")
			return false
		}
	}
	return true
}

func parseLimit(s string) (int, bool) {
	if s == "" {
		return 50, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= 200
}

func parseKinds(kinds []string) ([]string, bool) {
	if len(kinds) > 10 {
		return nil, false
	}
	var types []string
	for _, k := range kinds {
		t := library.JellyfinTypes(k)
		if len(t) == 0 {
			return nil, false
		}
		types = append(types, t...)
	}
	return types, true
}

var seedValue = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)

var sorts = map[string]string{
	"name":            "SortName",
	"premiereDate":    "PremiereDate",
	"addedAt":         "DateCreated",
	"communityRating": "CommunityRating",
	"lastPlayed":      "DatePlayed",
}

// nameValue accepts genre and studio names: printable, short, and without the
// "|" Jellyfin uses to separate them.
var nameValue = regexp.MustCompile(`^[^|\x00-\x1f\x7f]{1,128}$`)

func parseItemQuery(q url.Values) (jellyfin.ItemQuery, bool) {
	var jq jellyfin.ItemQuery
	var ok bool

	if types, good := parseKinds(q["kind"]); good {
		jq.Types = types
	} else {
		return jq, false
	}
	switch {
	case q.Get("parent") != "":
		if jq.ParentID, ok = library.Unwrap(library.ItemPrefix, q.Get("parent")); !ok {
			return jq, false
		}
	case q.Get("library") != "":
		if jq.ParentID, ok = library.Unwrap(library.LibraryPrefix, q.Get("library")); !ok {
			return jq, false
		}
		jq.Recursive = true
	default:
		jq.Recursive = true
	}
	if g := q["genre"]; len(g) > 10 {
		return jq, false
	}
	for _, g := range q["genre"] {
		if !nameValue.MatchString(g) {
			return jq, false
		}
		jq.Genres = append(jq.Genres, g)
	}
	if s := q.Get("studio"); s != "" {
		if !nameValue.MatchString(s) {
			return jq, false
		}
		jq.Studios = []string{s}
	}
	if p := q.Get("person"); p != "" {
		if jq.PersonID, ok = library.Unwrap(library.PersonPrefix, p); !ok {
			return jq, false
		}
	}
	for param, dst := range map[string]*string{"yearFrom": &jq.MinDate, "yearTo": &jq.MaxDate} {
		if s := q.Get(param); s != "" {
			y, err := strconv.Atoi(s)
			if err != nil || y < 1800 || y > 9999 {
				return jq, false
			}
			if param == "yearFrom" {
				*dst = strconv.Itoa(y) + "-01-01"
			} else {
				*dst = strconv.Itoa(y) + "-12-31"
			}
		}
	}
	for param, dst := range map[string]**bool{"played": &jq.IsPlayed, "favorite": &jq.IsFavorite} {
		if s := q.Get(param); s != "" {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return jq, false
			}
			*dst = &b
		}
	}
	if s := q.Get("inProgress"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return jq, false
		}
		jq.Resumable = b
	}
	switch s := q.Get("sort"); s {
	case "":
		jq.SortBy = "SortName"
	case "random":
		jq.SortBy = "random" // shuffled by Overfin, see randomPage
	default:
		if jq.SortBy, ok = sorts[s]; !ok {
			return jq, false
		}
	}
	switch q.Get("order") {
	case "", "asc":
	case "desc":
		jq.Descending = true
	default:
		return jq, false
	}
	if s := q.Get("seed"); s != "" && (jq.SortBy != "random" || !seedValue.MatchString(s)) {
		return jq, false
	}
	return jq, true
}

func writeOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
