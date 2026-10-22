package api

import (
	"context"
	"net/http"
	"time"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// What each profile does with each title is Overfin's to keep (SECURITY.md
// rule 40): playbacks record where it stopped and what it watched, and the
// profile marks titles watched or favorites itself. Jellyfin keeps none of
// it: its one account plays for everyone.

// When a playback counts: under resumeFrom of a title, nothing is recorded
// (a peek); from there to watchedFrom, where it stopped; past watchedFrom,
// or at the end, it's watched once more, when the playback ends.
const (
	resumeFrom  = 0.05
	watchedFrom = 0.90
)

// record keeps where the profile is in the playback's title, at
// positionMs; ending says the playback is over (only then is a title
// watched).
func (pb Playback) record(ctx context.Context, s *playback.Stream, positionMs int64, ending bool) {
	duration := s.Decision.Source.RunTimeTicks / 10_000
	if duration <= 0 {
		return
	}
	done := float64(positionMs) / float64(duration)
	finished := false
	switch {
	case done < resumeFrom:
		return
	case done >= watchedFrom:
		if !ending {
			return
		}
		finished = true
	}
	st := pb.Library.Provisioner.Store
	keys, err := st.TitleKeys(ctx, []string{s.ItemID})
	if err == nil {
		err = st.RecordPlay(ctx, s.ProfileID, keys[s.ItemID], s.ItemID, positionMs, finished, time.Now())
	}
	if err != nil {
		pb.Logger.Error("recording where a profile is in a title failed", "playback", s.ID, "err", err)
	}
}

// markPlayed and the others mark the item the request names, for the
// active profile: watched or not (on a series or a season, every episode
// the library has of it), a favorite or not. The item is read first, and
// checked, so nothing the profile may not see is marked.
func (l Library) markPlayed(w http.ResponseWriter, r *http.Request)     { l.mark(w, r, true, false) }
func (l Library) unmarkPlayed(w http.ResponseWriter, r *http.Request)   { l.mark(w, r, false, false) }
func (l Library) markFavorite(w http.ResponseWriter, r *http.Request)   { l.mark(w, r, true, true) }
func (l Library) unmarkFavorite(w http.ResponseWriter, r *http.Request) { l.mark(w, r, false, true) }

func (l Library) mark(w http.ResponseWriter, r *http.Request, on, favorite bool) {
	if !onlyParams(w, r) {
		return
	}
	id, ok := library.Unwrap(library.ItemPrefix, r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	ja, uid, ok := l.account(w, r)
	if !ok {
		return
	}
	raw, err := l.Jellyfin.Item(ctx, ja, uid, id)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	if may, err := l.mayOpen(ctx, p, raw); err != nil || !may {
		l.hidden(w, err)
		return
	}
	item, ok := library.Summary(raw)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st := l.Provisioner.Store
	now := time.Now()
	ids := []string{id}
	if !favorite && (item.Kind == "series" || item.Kind == "season") {
		if ids, err = st.CatalogEpisodeIDs(ctx, id); err != nil {
			l.storeError(w, err)
			return
		}
	}
	keys, err := st.TitleKeys(ctx, append(ids, id))
	if err != nil {
		l.storeError(w, err)
		return
	}
	if favorite {
		err = st.SetFavorite(ctx, p.ID, keys[id], id, on, now)
	} else {
		titles := map[string]string{}
		for _, i := range ids {
			titles[keys[i]] = i
		}
		err = st.SetPlayed(ctx, p.ID, titles, on, now)
	}
	if err != nil {
		l.storeError(w, err)
		return
	}
	state, err := l.userState(ctx, p, keys[id])
	if err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, state)
}

// userState is what the profile did with a title, as the API gives it.
func (l Library) userState(ctx context.Context, p *store.Profile, title string) (library.UserState, error) {
	states, err := l.Provisioner.Store.PersonalStates(ctx, p.ID, []string{title})
	if err != nil {
		return library.UserState{}, err
	}
	s := states[title]
	out := library.UserState{PositionMs: s.PositionMs, Played: s.Played, PlayCount: s.PlayCount, Favorite: s.Favorite}
	if !s.LastPlayedAt.IsZero() {
		out.LastPlayedAt = s.LastPlayedAt.UTC().Format(time.RFC3339)
	}
	return out, nil
}

func (l Library) storeError(w http.ResponseWriter, err error) {
	l.Logger.Error("reading or writing a profile's titles failed", "err", err)
	problem.Send(w, problem.Internal, "")
}

// withStates puts on items what Overfin keeps about the profile's titles,
// in place of what Jellyfin said (rule 40). Series and seasons don't carry
// an unwatched count from it yet.
func (l Library) withStates(ctx context.Context, p *store.Profile, items []library.ItemSummary) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if id, ok := library.Unwrap(library.ItemPrefix, it.ID); ok {
			ids = append(ids, id)
		}
	}
	st := l.Provisioner.Store
	keys, err := st.TitleKeys(ctx, ids)
	if err != nil {
		return err
	}
	titles := make([]string, 0, len(keys))
	for _, k := range keys {
		titles = append(titles, k)
	}
	states, err := st.PersonalStates(ctx, p.ID, titles)
	if err != nil {
		return err
	}
	for i := range items {
		id, _ := library.Unwrap(library.ItemPrefix, items[i].ID)
		s := states[keys[id]]
		us := library.UserState{PositionMs: s.PositionMs, Played: s.Played, PlayCount: s.PlayCount, Favorite: s.Favorite}
		if !s.LastPlayedAt.IsZero() {
			us.LastPlayedAt = s.LastPlayedAt.UTC().Format(time.RFC3339)
		}
		items[i].UserState = &us
	}
	// And Overfin's artwork, wherever items are shown.
	return l.withArtwork(ctx, items)
}

// forgetProgress takes a title out of the profile's Continue Watching.
func (l Library) forgetProgress(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	id, ok := library.Unwrap(library.ItemPrefix, r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	ja, uid, ok := l.account(w, r)
	if !ok {
		return
	}
	raw, err := l.Jellyfin.Item(ctx, ja, uid, id)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	if may, err := l.mayOpen(ctx, p, raw); err != nil || !may {
		l.hidden(w, err)
		return
	}
	st := l.Provisioner.Store
	keys, err := st.TitleKeys(ctx, []string{id})
	if err == nil {
		err = st.ForgetProgress(ctx, p.ID, keys[id])
	}
	if err != nil {
		l.storeError(w, err)
		return
	}
	state, err := l.userState(ctx, p, keys[id])
	if err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, state)
}

// clearHistory forgets what the active profile did with every title. A
// kids profile can't (its account's owner manages it).
func (l Library) clearHistory(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	if p.Kind == "kids" {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if err := l.Provisioner.Store.ClearPersonal(r.Context(), p.ID); err != nil {
		l.storeError(w, err)
		return
	}
	l.Logger.Info("a profile cleared its history", "profile", p.ID)
	w.WriteHeader(http.StatusNoContent)
}
