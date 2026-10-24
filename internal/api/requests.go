package api

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
)

// Requests asks Seerr for titles no library has (milestone 5, step 5), on
// behalf of the account: each has a Seerr user of its own, under the name
// the control panel shows for the account (a placeholder address on
// .invalid, a password no one keeps), which Seerr's own settings give its
// permissions, auto-approval and quotas. Requests belong to the profile that made them; the owner
// also sees the kids profiles'. Offered while Seerr is connected.
type Requests struct {
	Library Library
	// Seerr is the connected Seerr, nil when there's none.
	Seerr func() *seerr.Client
	// Directory, when set, is the identity provider's connector: people's
	// names come from it first.
	Directory connector.Directory
	// Limit, when set, limits requests and cancellations per account.
	Limit *AccountLimiter
}

func (Requests) Name() string { return "requests" }

func (q Requests) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/requests":         q.list,
		"POST /api/v1/requests":        q.create,
		"DELETE /api/v1/requests/{id}": q.cancel,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

type requestJSON struct {
	ID          string               `json:"id"`
	Title       library.TitleSummary `json:"title"`
	Seasons     []int                `json:"seasons,omitempty"`
	Status      string               `json:"status"`
	RequestedAt string               `json:"requestedAt"`
	ProfileName string               `json:"profileName,omitempty"`
}

// statusOf is a request's status in Overfin's words: Seerr's request
// status first (pending, declined, failed), then the title's.
func statusOf(r seerr.Request) string {
	switch r.Status {
	case 1:
		return "pending"
	case 3:
		return "declined"
	case 4:
		return "failed"
	}
	switch r.MediaStatus {
	case 3:
		return "processing"
	case 4:
		return "partiallyAvailable"
	case 5:
		return "available"
	}
	return "approved"
}

// settled are the statuses that don't change any more.
var settled = []string{"declined", "available", "failed"}

// client is the connected Seerr, or writes "not found" (requests aren't
// offered) and returns nil.
func (q Requests) client(w http.ResponseWriter) *seerr.Client {
	var c *seerr.Client
	if q.Seerr != nil {
		c = q.Seerr()
	}
	if c == nil {
		problem.Send(w, problem.NotFound, "")
	}
	return c
}

// seerrUser is the account's Seerr user, made now if it has none or Seerr
// lost it, and renamed when the account's name changed.
func (q Requests) seerrUser(ctx context.Context, c *seerr.Client, a *store.Account) (int, error) {
	st := q.Library.Provisioner.Store
	name, sure, err := q.nameOf(ctx, a)
	if err != nil {
		return 0, err
	}
	id, ok, err := st.SeerrUser(ctx, a.ID)
	if err != nil {
		return 0, err
	}
	if ok {
		had, exists, err := c.Username(ctx, id)
		if err != nil {
			return 0, err
		}
		if exists {
			// Not renamed on a guess: while the identity provider can't
			// be asked, the name stays what it was.
			// A failed rename doesn't hold the request up: it's tried
			// again at the next one.
			if sure && had != name {
				if err := c.Rename(ctx, id, name); err != nil {
					q.Library.Logger.Warn("requests: renaming the Seerr user failed", "err", err)
				}
			}
			return id, nil
		}
	}
	// The address is a placeholder no one can write to, and differs for
	// each user, as Seerr wants.
	password := random(24) // thrown away: no one signs in to Seerr as them
	if id, err = c.CreateUser(ctx, name, "overfin-"+random(9)+"@overfin.invalid", password); err != nil {
		return 0, err
	}
	return id, st.SetSeerrUser(ctx, a.ID, id, time.Now())
}

// maxName caps a name given to Seerr, in characters.
const maxName = 64

// nameOf is the account's name as the control panel shows it: the
// identity provider's (its name, else its username), else the label the
// operator gave it, else its owner profile's. sure is false when the
// identity provider couldn't be asked: the name is then a fallback.
func (q Requests) nameOf(ctx context.Context, a *store.Account) (name string, sure bool, err error) {
	sure = true
	if q.Directory != nil {
		people, err := q.Directory.People(ctx)
		if err != nil {
			sure = false
		}
		if p, ok := people[a.Subject]; ok {
			name = cmp.Or(strings.TrimSpace(p.Name), strings.TrimSpace(p.Username))
		}
	}
	if name == "" {
		name = strings.TrimSpace(a.Label)
	}
	if name == "" {
		profiles, err := q.Library.Provisioner.Store.Profiles(ctx, a.ID)
		if err != nil {
			return "", false, err
		}
		for _, p := range profiles {
			if p.Owner {
				name = strings.TrimSpace(p.Name)
			}
		}
	}
	if r := []rune(name); len(r) > maxName {
		name = string(r[:maxName])
	}
	return cmp.Or(name, "Overfin"), sure, nil
}

// random is n random bytes, as lowercase letters and digits.
func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return strings.ToLower(strings.NewReplacer("-", "a", "_", "b").Replace(base64.RawURLEncoding.EncodeToString(b)))
}

func (q Requests) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TitleID string `json:"titleId"`
		Seasons []int  `json:"seasons"`
	}
	if !decode(r, &body) || len(body.Seasons) > 100 {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	for _, n := range body.Seasons {
		if n < 0 || n > 1000 {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
	}
	l := q.Library
	caller := l.Sessions.Current(w, r)
	if caller == nil {
		return
	}
	if q.Limit != nil && !q.Limit.allow(caller.Account.ID, time.Now()) {
		problem.Send(w, problem.RateLimited, "")
		return
	}
	c := q.client(w)
	if c == nil {
		return
	}
	ctx := r.Context()
	p := caller.Profile
	ref, ok := titleRef(body.TitleID)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	// What Seerr is asked for: a film, or seasons of a series (a season or
	// an episode means its season).
	kind, tmdbID, seasons := ref.Kind, ref.TMDB(), body.Seasons
	switch {
	case tmdbID == "" || ref.Kind == "item":
		problem.Send(w, problem.InvalidRequest, "only titles known to TMDB can be requested")
		return
	case ref.Kind == "movie" && len(seasons) > 0:
		problem.Send(w, problem.InvalidRequest, "")
		return
	case ref.Kind == "season" || ref.Kind == "episode":
		if len(seasons) > 0 {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
		kind, seasons = "series", []int{ref.Season}
	}
	l.describe(ctx, ref)
	t, ok := l.visibleTitleRef(w, ctx, p, ref)
	if !ok {
		return
	}
	if availability(t) == "available" {
		problem.Send(w, problem.AlreadyRequested, "")
		return
	}
	userID, err := q.seerrUser(ctx, c, caller.Account)
	if err != nil {
		q.seerrError(w, err)
		return
	}
	n, _ := strconv.Atoi(tmdbID)
	made, err := c.RequestTitle(ctx, userID, kind, n, seasons)
	if q.Library.Requested != nil {
		q.Library.Requested.Stale() // asked, or already asked by someone
	}
	if err != nil {
		q.seerrError(w, err)
		return
	}
	req := store.Request{AccountID: caller.Account.ID, ProfileID: p.ID, Title: kind + "/tmdb/" + tmdbID, Seasons: seasons,
		SeerrID: made.ID, Status: statusOf(made), CreatedAt: time.Now()}
	if err := l.Provisioner.Store.AddRequest(ctx, &req); err != nil {
		l.storeError(w, err)
		return
	}
	out, err := q.render(ctx, caller, []store.Request{req})
	if err != nil || len(out) == 0 {
		l.storeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(out[0])
}

// seerrError answers for a failed call to Seerr.
func (q Requests) seerrError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, seerr.ErrQuota):
		problem.Send(w, problem.QuotaExceeded, "")
	case errors.Is(err, seerr.ErrExists):
		problem.Send(w, problem.AlreadyRequested, "")
	case errors.Is(err, seerr.ErrRefused):
		problem.Send(w, problem.RequestRefused, "")
	default:
		q.Library.Logger.Error("Seerr call failed", "err", err)
		problem.Send(w, problem.Internal, "")
	}
}

// visibleProfiles are the profiles whose requests the caller sees: its
// own, and for the owner, the kids profiles too.
func (q Requests) visibleProfiles(ctx context.Context, c *Caller) (ids []string, names map[string]string, err error) {
	ids, names = []string{c.Profile.ID}, map[string]string{}
	if !c.IsOwner() {
		return ids, names, nil
	}
	all, err := q.Library.Provisioner.Store.Profiles(ctx, c.Account.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range all {
		if p.Kind == "kids" && p.ID != c.Profile.ID {
			ids = append(ids, p.ID)
			names[p.ID] = p.Name
		}
	}
	return ids, names, nil
}

func (q Requests) list(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	l := q.Library
	caller := l.Sessions.Current(w, r)
	if caller == nil {
		return
	}
	c := q.client(w)
	if c == nil {
		return
	}
	ctx := r.Context()
	profiles, _, err := q.visibleProfiles(ctx, caller)
	if err != nil {
		l.storeError(w, err)
		return
	}
	st := l.Provisioner.Store
	all, err := st.ProfileRequests(ctx, caller.Account.ID, profiles)
	if err != nil {
		l.storeError(w, err)
		return
	}
	// The statuses that may have changed, as Seerr has them now; when it
	// can't answer, the last known ones.
	if slices.ContainsFunc(all, func(x store.Request) bool { return !slices.Contains(settled, x.Status) }) {
		if userID, ok, _ := st.SeerrUser(ctx, caller.Account.ID); ok {
			if now, err := c.UserRequests(ctx, userID); err == nil {
				byID := map[int]seerr.Request{}
				for _, x := range now {
					byID[x.ID] = x
				}
				for i := range all {
					if x, ok := byID[all[i].SeerrID]; ok && statusOf(x) != all[i].Status {
						all[i].Status = statusOf(x)
						st.SetRequestStatus(ctx, all[i].ID, all[i].Status, time.Now())
					}
				}
			}
		}
	}
	out, err := q.render(ctx, caller, all)
	if err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, map[string]any{"items": out})
}

// render gives requests as the API shows them, with their titles (those
// the caller may still see).
func (q Requests) render(ctx context.Context, c *Caller, all []store.Request) ([]requestJSON, error) {
	_, names, err := q.visibleProfiles(ctx, c)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(all))
	for _, x := range all {
		keys = append(keys, x.Title)
	}
	titles, err := q.Library.titleSummaries(ctx, c.Profile, keys)
	if err != nil {
		return nil, err
	}
	byID := map[string]library.TitleSummary{}
	for _, t := range titles {
		byID[t.ID] = t
	}
	out := []requestJSON{}
	for _, x := range all {
		t, ok := byID[titleID(x.Title)]
		if !ok {
			continue
		}
		out = append(out, requestJSON{ID: x.ID, Title: t, Seasons: x.Seasons, Status: x.Status,
			RequestedAt: x.CreatedAt.UTC().Format(time.RFC3339), ProfileName: names[x.ProfileID]})
	}
	return out, nil
}

func (q Requests) cancel(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	l := q.Library
	caller := l.Sessions.Current(w, r)
	if caller == nil {
		return
	}
	if q.Limit != nil && !q.Limit.allow(caller.Account.ID, time.Now()) {
		problem.Send(w, problem.RateLimited, "")
		return
	}
	c := q.client(w)
	if c == nil {
		return
	}
	ctx := r.Context()
	st := l.Provisioner.Store
	x, err := st.RequestByID(ctx, caller.Account.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		problem.Send(w, problem.NotFound, "")
		return
	} else if err != nil {
		l.storeError(w, err)
		return
	}
	profiles, _, err := q.visibleProfiles(ctx, caller)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if !slices.Contains(profiles, x.ProfileID) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if x.Status != "pending" {
		problem.Send(w, problem.NotPending, "")
		return
	}
	userID, err := q.seerrUser(ctx, c, caller.Account)
	if err != nil {
		q.seerrError(w, err)
		return
	}
	if err := c.CancelRequest(ctx, userID, x.SeerrID); err != nil && !errors.Is(err, seerr.ErrNotFound) {
		q.seerrError(w, err)
		return
	}
	if err := st.DeleteRequest(ctx, caller.Account.ID, x.ID); err != nil {
		l.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
