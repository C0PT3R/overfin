package api

import (
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Search finds titles, in a library or not, and people (POST /search). The
// terms travel in the body, never in a URL (SECURITY.md rule 48), and
// searches are rate-limited per account (rule 31).

type searchPerson struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Department string            `json:"department,omitempty"`
	Image      *library.ImageRef `json:"image,omitempty"`
}

type searchResults struct {
	// Titles are those a library has first, then the others, as TMDB
	// ranks them.
	Titles []library.TitleSummary `json:"titles"`
	People []searchPerson         `json:"people"`
	// PeopleFirst: the search looks like a person's name (TMDB ranked a
	// person first, or someone has that very name, and no library title
	// has it), so people are best shown before titles.
	PeopleFirst bool `json:"peopleFirst,omitempty"`
}

// AccountLimiter allows each account a number of actions a minute
// (searches, requests).
type AccountLimiter struct {
	PerMinute int
	mu        sync.Mutex
	recent    map[string][]time.Time
}

// allow says whether an account may act now, and counts it.
func (s *AccountLimiter) allow(account string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recent == nil {
		s.recent = map[string][]time.Time{}
	}
	kept := s.recent[account][:0]
	for _, t := range s.recent[account] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= s.PerMinute {
		s.recent[account] = kept
		return false
	}
	s.recent[account] = append(kept, now)
	// Forget accounts that haven't acted for a minute, now and then.
	if len(s.recent) > 1000 {
		for a, times := range s.recent {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= time.Minute {
				delete(s.recent, a)
			}
		}
	}
	return true
}

func (l Library) search(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	if !decode(r, &body) || utf8.RuneCountInString(body.Query) > 200 {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	caller := l.Sessions.Current(w, r)
	if caller == nil {
		return
	}
	p := caller.Profile
	out := searchResults{Titles: []library.TitleSummary{}, People: []searchPerson{}}
	query := strings.TrimSpace(body.Query)
	if utf8.RuneCountInString(query) < 2 {
		writeOK(w, out)
		return
	}
	if l.Searches != nil && !l.Searches.allow(caller.Account.ID, time.Now()) {
		problem.Send(w, problem.RateLimited, "")
		return
	}
	ctx := r.Context()
	keys, err := l.Provisioner.Store.SearchLibrary(ctx, query, 30)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if l.Titles != nil {
		titles, people, first, err := l.Titles.Search(ctx, query)
		out.PeopleFirst = first
		if err != nil {
			l.Logger.Warn("searching TMDB failed", "err", err) // the library's results still
		}
		for _, t := range titles {
			keys = append(keys, t.Kind+"/tmdb/"+t.TMDB)
		}
		for _, h := range people {
			if store.Fold(h.Name) == store.Fold(query) {
				out.PeopleFirst = true
			}
			sp := searchPerson{ID: personID(h.TMDB), Name: h.Name, Department: h.Department}
			if img := images.ArtworkID("profile", h.Profile); img != "" {
				sp.Image = &library.ImageRef{URL: "/api/v1/images/" + img}
			}
			out.People = append(out.People, sp)
		}
	}
	all, err := l.titleSummaries(ctx, p, keys)
	if err != nil {
		l.storeError(w, err)
		return
	}
	// The library's first, then the others, each in its order.
	for _, t := range all {
		if t.Availability != "missing" {
			out.Titles = append(out.Titles, t)
			if store.Fold(t.Name) == store.Fold(query) {
				out.PeopleFirst = false
			}
		}
	}
	for _, t := range all {
		if t.Availability == "missing" {
			out.Titles = append(out.Titles, t)
		}
	}
	writeOK(w, out)
}
