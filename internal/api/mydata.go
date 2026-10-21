package api

import (
	"context"
	"net/http"
	"time"

	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/library"
	"github.com/C0PT3R/overfin-server/internal/problem"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// ---- Deleting an account ----------------------------------------------------

// deleteAccount deletes the account and everything Overfin holds about it
// (rule 45). Its Jellyfin users are deleted with their history, or disabled
// and kept, as the operator chose; the answer says which. Owner only, after a
// fresh login.
func (a Account) deleteAccount(w http.ResponseWriter, r *http.Request) {
	c := a.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if !a.Sessions.RequireFresh(w, c) {
		return
	}
	profiles, err := a.Provisioner.DeleteAccount(r.Context(), c.Account.ID, a.KeepOnDelete)
	if err != nil {
		a.Logger.Error("deleting an account failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	for _, p := range profiles {
		if a.Images != nil {
			a.Images.Forget(p.ID)
		}
	}
	clearCookie(w)
	out := map[string]string{"history": "deleted"}
	if a.KeepOnDelete {
		out["history"] = "kept"
	}
	if auth := a.Sessions.Auth; auth != nil && auth.Login {
		auth.Logout(w, r)
	}
	writeOK(w, out)
}

// ---- Downloading your data --------------------------------------------------

type exportJSON struct {
	ExportedAt string              `json:"exportedAt"`
	Account    *exportAccountJSON  `json:"account,omitempty"`
	Profiles   []exportProfileJSON `json:"profiles"`
	Sessions   []exportSessionJSON `json:"sessions"`
}

type exportAccountJSON struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
}

type exportProfileJSON struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Owner         bool   `json:"owner"`
	Language      string `json:"language,omitempty"`
	MovieCeiling  string `json:"movieCeiling,omitempty"`
	SeriesCeiling string `json:"seriesCeiling,omitempty"`
	HasPIN        bool   `json:"hasPin"`
	CreatedAt     string `json:"createdAt"`
	// WatchHistory is read from the media server for this download.
	WatchHistory []exportItemJSON `json:"watchHistory"`
	// Titles is what Overfin keeps about what the profile did with each
	// title: where it stopped, whether it watched it, how many times and
	// when last, its favorites.
	Titles []exportTitleJSON `json:"titles"`
	// Modules holds what each switched-on module keeps about the profile,
	// by module name (the watchlist, say).
	Modules map[string]any `json:"modules,omitempty"`
}

type exportTitleJSON struct {
	Title        string `json:"title"`
	Item         string `json:"item,omitempty"`
	PositionMs   int64  `json:"positionMs,omitempty"`
	Played       bool   `json:"played"`
	PlayCount    int    `json:"playCount,omitempty"`
	LastPlayedAt string `json:"lastPlayedAt,omitempty"`
	Favorite     bool   `json:"favorite"`
}

type exportItemJSON struct {
	library.ItemSummary
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
}

type exportSessionJSON struct {
	ID           string `json:"id"`
	Device       string `json:"device,omitempty"`
	ProfileName  string `json:"profileName,omitempty"`
	CreatedAt    string `json:"createdAt"`
	LastActiveAt string `json:"lastActiveAt,omitempty"`
}

// export answers with what Overfin holds about the account (from the owner
// profile) or about the active profile only (from any other), plus the watch
// history read live, never secrets (rule 52). The whole account needs the
// account holder's fresh login; a profile's own data needs only that
// profile's PIN, if it has one: the people using other profiles don't know
// the account's login, and without a PIN the profile is open to anyone at
// the device anyway. Kids profiles can't export themselves: their data is
// in the owner's export.
func (a Account) export(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN string `json:"pin"`
	}
	if r.ContentLength != 0 && !decode(r, &req) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := a.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if c.Profile.Kind == "kids" {
		// A child's data is the account holder's to handle: it's in the
		// owner's whole-account export.
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if c.IsOwner() {
		if !a.Sessions.RequireFresh(w, c) {
			return
		}
	} else if !checkPIN(w, r.Context(), a.PINs, a.Logger, c.Account.ID, c.Profile.ID, req.PIN) {
		return
	}
	ctx := r.Context()
	profiles := []*store.Profile{c.Profile}
	out := exportJSON{ExportedAt: time.Now().UTC().Format(time.RFC3339), Profiles: []exportProfileJSON{}, Sessions: []exportSessionJSON{}}
	if c.IsOwner() {
		var err error
		if profiles, err = a.Sessions.Store.Profiles(ctx, c.Account.ID); err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
		out.Account = &exportAccountJSON{ID: c.Account.ID, CreatedAt: c.Account.Created.Format(time.RFC3339)}
	}
	names := map[string]string{}
	for _, p := range profiles {
		names[p.ID] = p.Name
		history, err := a.history(ctx, p)
		if err != nil {
			a.Logger.Error("reading watch history for an export failed", "err", err)
			problem.Send(w, problem.Internal, "")
			return
		}
		kept, err := a.Sessions.Store.AllPersonal(ctx, p.ID)
		if err != nil {
			a.Logger.Error("reading a profile's titles for an export failed", "err", err)
			problem.Send(w, problem.Internal, "")
			return
		}
		titles := make([]exportTitleJSON, 0, len(kept))
		for _, k := range kept {
			t := exportTitleJSON{Title: k.Title, PositionMs: k.PositionMs, Played: k.Played, PlayCount: k.PlayCount, Favorite: k.Favorite}
			if k.ItemID != "" {
				t.Item = "item_" + k.ItemID
			}
			if !k.LastPlayedAt.IsZero() {
				t.LastPlayedAt = k.LastPlayedAt.UTC().Format(time.RFC3339)
			}
			titles = append(titles, t)
		}
		mods := map[string]any{}
		as := &Caller{Account: c.Account, Session: c.Session, Profile: p, Owner: c.Owner}
		for _, m := range a.Mounts {
			data, ok, err := m.Export(ctx, as)
			if err != nil {
				a.Logger.Error("a module's part of an export failed", "module", m.Name(), "err", err)
				problem.Send(w, problem.Internal, "")
				return
			}
			if ok {
				mods[m.Name()] = data
			}
		}
		out.Profiles = append(out.Profiles, exportProfileJSON{
			ID: p.ID, Name: p.Name, Kind: p.Kind, Owner: p.Owner, Language: p.Language,
			MovieCeiling: p.MovieCeiling, SeriesCeiling: p.SeriesCeiling, HasPIN: p.HasPIN, CreatedAt: p.Created.Format(time.RFC3339),
			WatchHistory: history, Titles: titles, Modules: mods,
		})
	}
	sessions, err := a.Sessions.Store.Sessions(ctx, c.Account.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	for _, s := range sessions {
		active := s.ActiveProfileID
		if active == "" {
			active = c.Owner.ID
		}
		if _, ours := names[active]; !ours {
			continue // another profile's session (rule 51)
		}
		out.Sessions = append(out.Sessions, exportSessionJSON{
			ID: s.ID, Device: s.Device, ProfileName: names[active],
			CreatedAt: s.Created.Format(time.RFC3339), LastActiveAt: s.LastActive.Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Disposition", `attachment; filename="my-data.json"`)
	writeOK(w, out)
}

// historyLimit caps what one export reads per list, per profile.
const historyLimit = 10_000

// history reads a profile's watched, in-progress and favorite items from
// Jellyfin, with its own token. Jellyfin's filters combine with "and", so
// it takes one pass each.
func (a Account) history(ctx context.Context, p *store.Profile) ([]exportItemJSON, error) {
	jf := a.Provisioner.Jellyfin
	yes := true
	passes := []jellyfin.ItemQuery{
		{IsPlayed: &yes},
		{Resumable: true},
		{IsFavorite: &yes},
	}
	seen := map[string]bool{}
	out := []exportItemJSON{}
	for _, q := range passes {
		q.Recursive, q.SortBy, q.Descending, q.ExtraFields = true, "DatePlayed", true, "ProviderIds"
		for start := 0; start < historyLimit; start += 500 {
			q.StartIndex, q.Limit = start, 500
			page, err := jf.Items(ctx, auth(p), p.JellyfinUserID, q)
			if err != nil {
				return nil, err
			}
			for i := range page.Items {
				raw := &page.Items[i]
				s, ok := library.Summary(raw)
				if !ok || seen[s.ID] {
					continue
				}
				seen[s.ID] = true
				out = append(out, exportItemJSON{ItemSummary: s, ExternalIDs: library.ExternalIDs(raw.ProviderIds)})
			}
			if len(page.Items) < 500 {
				break
			}
		}
	}
	return out, nil
}
