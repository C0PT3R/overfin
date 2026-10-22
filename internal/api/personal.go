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
