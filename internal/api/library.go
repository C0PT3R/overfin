package api

import (
	"context"
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

	"github.com/C0PT3R/overfin/internal/cursor"
	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/images"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/library"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/store"
)

// Library serves browsing: libraries, item queries, details and the home
// rows. Every call runs with the active profile's own Jellyfin token, so
// Jellyfin itself enforces library access and parental limits.
type Library struct {
	Provisioner *provision.Provisioner
	Jellyfin    *jellyfin.Client
	Images      *images.Pipeline
	CursorKey   []byte
	Logger      *slog.Logger
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
		"GET /api/v1/images/{id}":   l.image,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// active returns the caller's active profile, or writes the problem and
// returns nil. Until profiles can be switched, it is the owner profile.
func (l Library) active(w http.ResponseWriter, r *http.Request) *store.Profile {
	id, _ := identity.FromContext(r.Context())
	_, prof, err := l.Provisioner.Find(r.Context(), id)
	switch {
	case err == nil:
		return prof
	case errors.Is(err, provision.ErrNoAccount):
		problem.Send(w, problem.AccountSetupRequired, "")
	case errors.Is(err, provision.ErrNotAllowed):
		problem.Send(w, problem.AccountNotAllowed, "")
	case errors.Is(err, provision.ErrAnonymous):
		problem.Send(w, problem.NotFound, "")
	default:
		l.Logger.Error("profile lookup failed", "err", err)
		problem.Send(w, problem.Internal, "")
	}
	return nil
}

func auth(p *store.Profile) jellyfin.Auth {
	return jellyfin.Auth{Token: p.JellyfinToken, DeviceID: "overfin-" + p.JellyfinUserID}
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
	l.page(w, r, p, func(ctx context.Context, start, limit int) (*jellyfin.ItemPage, error) {
		jq.StartIndex, jq.Limit = start, limit
		return l.Jellyfin.Items(ctx, auth(p), p.JellyfinUserID, jq)
	})
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
	next := start + len(res.Items)
	if len(res.Items) == limit && next < res.TotalRecordCount {
		c, err := cursor.Encode(l.CursorKey, binding, []byte(strconv.Itoa(next)))
		if err == nil {
			out.NextCursor = c
		}
	}
	// Jellyfin counts with the profile's own token, so hidden items are
	// already excluded (rule 56).
	total := res.TotalRecordCount
	out.TotalCount = &total
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
		return jq, false // repeatable random order isn't supported yet
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
	if q.Get("seed") != "" {
		return jq, false
	}
	return jq, true
}

func writeOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
