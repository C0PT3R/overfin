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

	"github.com/C0PT3R/overfin-server/internal/cursor"
	"github.com/C0PT3R/overfin-server/internal/images"
	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/library"
	"github.com/C0PT3R/overfin-server/internal/plugin"
	"github.com/C0PT3R/overfin-server/internal/problem"
	"github.com/C0PT3R/overfin-server/internal/provision"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// Library serves browsing: libraries, item queries, details and the home
// rows. Every call runs with the active profile's own Jellyfin token, so
// Jellyfin itself enforces library access and parental limits.
type Library struct {
	Sessions    *Sessions
	Provisioner *provision.Provisioner
	Jellyfin    *jellyfin.Client
	Images      *images.Pipeline
	CursorKey   []byte
	Logger      *slog.Logger
	// Discovery, when set, serves the catalog's TMDB images (tmdb_…).
	Discovery *Discovery
}

func (Library) Name() string { return "library" }

func (l Library) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/libraries":     l.libraries,
		"GET /api/v1/items":         l.items,
		"GET /api/v1/items/resume":  l.resume,
		"GET /api/v1/items/next-up": l.nextUp,
		"GET /api/v1/items/latest":  l.latest,
		"GET /api/v1/items/{id}":    l.item,
		"GET /api/v1/people/{id}":   l.person,
		"GET /api/v1/images/{id}":   l.image,
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

func auth(p *store.Profile) jellyfin.Auth {
	return jellyfin.Auth{Token: p.JellyfinToken, DeviceID: provision.DeviceID(p.JellyfinUserID)}
}

// upstreamError answers for a failed Jellyfin call: "not found" when Jellyfin
// says so (which also covers items the profile may not see), a bare internal
// error otherwise. Jellyfin's own message never reaches the client.
func (l Library) upstreamError(w http.ResponseWriter, err error) {
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	l.Logger.Error("jellyfin call failed", "err", err)
	problem.Send(w, problem.Internal, "")
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
	raw, err := l.Jellyfin.Libraries(r.Context(), auth(p), p.JellyfinUserID)
	if err != nil {
		l.upstreamError(w, err)
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

// person serves a person's page. It's fetched with the profile's own token,
// like everything else.
func (l Library) person(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	jfID, ok := library.Unwrap(library.PersonPrefix, r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	raw, err := l.Jellyfin.Item(r.Context(), auth(p), p.JellyfinUserID, jfID)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	person, ok := library.PersonOf(raw)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, person)
}

func (l Library) item(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	jfID, ok := library.Unwrap(library.ItemPrefix, r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	raw, err := l.Jellyfin.Item(r.Context(), auth(p), p.JellyfinUserID, jfID)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	it, ok := library.Detail(raw)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, it)
}

func (l Library) items(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !onlyParams(w, r, "library", "kind", "parent", "genre", "person", "studio", "yearFrom", "yearTo",
		"played", "favorite", "inProgress", "sort", "order", "seed", "limit", "cursor") {
		return
	}
	jq, ok := parseItemQuery(q)
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	if jq.SortBy == "random" {
		l.randomPage(w, r, p, jq)
		return
	}
	if lq, ok := indexQuery(q, p); ok && l.indexReady(r) {
		l.indexPage(w, r, p, lq, jq)
		return
	}
	l.page(w, r, p, func(ctx context.Context, start, limit int) (*jellyfin.ItemPage, error) {
		jq.StartIndex, jq.Limit = start, limit
		return l.Jellyfin.Items(ctx, auth(p), p.JellyfinUserID, jq)
	})
}

// randomPage serves sort=random in a repeatable way. Jellyfin's own random
// order changes on every call, so pages would overlap and a reload would
// lose the shuffle. Instead, Overfin orders the matching IDs by
// HMAC(seed, id): the same seed always gives the same order. The seed comes
// from the URL, or is picked here and returned, so the site can put it in
// the URL and a reload shows the same shuffle.
func (l Library) randomPage(w http.ResponseWriter, r *http.Request, p *store.Profile, jq jellyfin.ItemQuery) {
	q := r.URL.Query()
	limit, ok := parseLimit(q.Get("limit"))
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	seed := q.Get("seed")
	binding := cursorBinding(r, p.ID)
	start := 0
	if c := q.Get("cursor"); c != "" {
		payload, err := cursor.Decode(l.CursorKey, binding, c)
		if err != nil {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
		// The payload is "start:seed"; the seed matters when it was picked
		// by the server rather than given in the URL.
		s, cseed, found := strings.Cut(string(payload), ":")
		n, err := strconv.Atoi(s)
		if !found || err != nil || n < 0 || !seedValue.MatchString(cseed) || (seed != "" && seed != cseed) {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
		start, seed = n, cseed
	}
	if seed == "" {
		seed = newSeed()
	}

	ids, err := l.Jellyfin.ItemIDs(r.Context(), auth(p), p.JellyfinUserID, jq)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	shuffle(ids, seed)

	out := itemPage{Items: []library.ItemSummary{}, Seed: seed}
	total := len(ids)
	out.TotalCount = &total
	if start < total {
		pageIDs := ids[start:min(start+limit, total)]
		raw, err := l.Jellyfin.ItemsByID(r.Context(), auth(p), p.JellyfinUserID, pageIDs)
		if err != nil {
			l.upstreamError(w, err)
			return
		}
		byID := make(map[string]*jellyfin.RawItem, len(raw))
		for i := range raw {
			if id, err := jellyfin.NormalizeID(raw[i].Id); err == nil {
				byID[id] = &raw[i]
			}
		}
		for _, id := range pageIDs {
			if it, ok := byID[id]; ok {
				if s, ok := library.Summary(it); ok {
					out.Items = append(out.Items, s)
				}
			}
		}
		if next := start + len(pageIDs); next < total {
			if c, err := cursor.Encode(l.CursorKey, binding, []byte(strconv.Itoa(next)+":"+seed)); err == nil {
				out.NextCursor = c
			}
		}
	}
	writeOK(w, out)
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

func (l Library) resume(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "kind", "limit", "cursor") {
		return
	}
	types, ok := parseKinds(r.URL.Query()["kind"])
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	l.page(w, r, p, func(ctx context.Context, start, limit int) (*jellyfin.ItemPage, error) {
		return l.Jellyfin.Resume(ctx, auth(p), p.JellyfinUserID, types, start, limit)
	})
}

func (l Library) nextUp(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "series", "limit", "cursor") {
		return
	}
	var seriesID string
	if s := r.URL.Query().Get("series"); s != "" {
		var ok bool
		if seriesID, ok = library.Unwrap(library.ItemPrefix, s); !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	l.page(w, r, p, func(ctx context.Context, start, limit int) (*jellyfin.ItemPage, error) {
		return l.Jellyfin.NextUp(ctx, auth(p), p.JellyfinUserID, seriesID, start, limit)
	})
}

func (l Library) latest(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "library", "limit") {
		return
	}
	q := r.URL.Query()
	limit, ok := parseLimit(q.Get("limit"))
	var parent string
	if lib := q.Get("library"); lib != "" && ok {
		parent, ok = library.Unwrap(library.LibraryPrefix, lib)
	}
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	raw, err := l.Jellyfin.Latest(r.Context(), auth(p), p.JellyfinUserID, parent, limit)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	out := []library.ItemSummary{}
	for i := range raw {
		if s, ok := library.Summary(&raw[i]); ok {
			out = append(out, s)
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
	if id := r.PathValue("id"); strings.HasPrefix(id, "tmdb_") {
		if l.Discovery == nil || !slices.Contains(images.Sizes, size) {
			problem.Send(w, problem.NotFound, "")
			return
		}
		if p := l.active(w, r); p != nil {
			l.Discovery.image(w, r, p, id, size)
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
	data, contentType, err := l.Images.Image(r.Context(), images.Viewer{
		ProfileID: p.ID, UserID: p.JellyfinUserID, Auth: auth(p),
	}, ref, size)
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

type itemPage struct {
	Items      []library.ItemSummary `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
	TotalCount *int                  `json:"totalCount,omitempty"`
	Seed       string                `json:"seed,omitempty"`
}

// page runs a paged Jellyfin query. The cursor carries the next start index
// and is signed and bound to this profile and this exact query (rule 38).
func (l Library) page(w http.ResponseWriter, r *http.Request, p *store.Profile,
	fetch func(ctx context.Context, start, limit int) (*jellyfin.ItemPage, error)) {

	q := r.URL.Query()
	limit, ok := parseLimit(q.Get("limit"))
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	binding := cursorBinding(r, p.ID)
	start := 0
	if c := q.Get("cursor"); c != "" {
		payload, err := cursor.Decode(l.CursorKey, binding, c)
		if err != nil {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
		if start, err = strconv.Atoi(string(payload)); err != nil || start < 0 {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
	}

	res, err := fetch(r.Context(), start, limit)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	out := itemPage{Items: []library.ItemSummary{}}
	for i := range res.Items {
		if s, ok := library.Summary(&res.Items[i]); ok {
			out.Items = append(out.Items, s)
		}
	}
	// Without a total, a full page means there may be more.
	next := start + len(res.Items)
	if len(res.Items) == limit && (res.NoCount || next < res.TotalRecordCount) {
		c, err := cursor.Encode(l.CursorKey, binding, []byte(strconv.Itoa(next)))
		if err == nil {
			out.NextCursor = c
		}
	}
	// Jellyfin counts with the profile's own token, so hidden items are
	// already excluded (rule 56).
	if !res.NoCount {
		total := res.TotalRecordCount
		out.TotalCount = &total
	}
	writeOK(w, out)
}

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
