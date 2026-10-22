package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// People, from TMDB (milestone 5): who they are, and the films and series
// they're in, those the libraries have first. Their titles follow the
// same rules as everywhere (what the profile may see, the titles shown).

const personPrefix = "person_tmdb_"

// personFresh is how long a person's page is kept before it's fetched
// again when opened.
const personFresh = 7 * 24 * time.Hour

func personID(tmdbID string) string { return personPrefix + tmdbID }

// personRef checks a person's ID and returns their TMDB ID.
func personRef(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, personPrefix)
	n, err := strconv.Atoi(rest)
	if !ok || err != nil || !tmdb.ValidID(n) || strconv.Itoa(n) != rest {
		return "", false
	}
	return rest, true
}

type personCreditJSON struct {
	Title library.TitleSummary `json:"title"`
	// Roles are their characters, or their jobs behind the camera.
	Roles []string `json:"roles,omitempty"`
}

type personJSON struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Biography  string             `json:"biography,omitempty"`
	Birthday   string             `json:"birthday,omitempty"`
	Deathday   string             `json:"deathday,omitempty"`
	Birthplace string             `json:"birthplace,omitempty"`
	Department string             `json:"department,omitempty"`
	Image      *library.ImageRef  `json:"image,omitempty"`
	Credits    []personCreditJSON `json:"credits"`
}

// person serves a person's page, fetched from TMDB when it's not kept, or
// kept for more than a week.
func (l Library) person(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	id, ok := personRef(r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	st := l.Provisioner.Store
	kept, err := st.PersonByTMDB(ctx, id)
	if err != nil {
		l.storeError(w, err)
		return
	}
	// Fetched when not kept, kept only as a search found them, or old.
	if (kept == nil || !kept.Page || time.Since(kept.FetchedAt) > personFresh) && l.Titles != nil {
		if err := l.Titles.FetchPerson(ctx, id); err != nil && !errors.Is(err, tmdb.ErrNotFound) {
			l.Logger.Warn("fetching a person from TMDB failed", "err", err)
		}
		if kept, err = st.PersonByTMDB(ctx, id); err != nil {
			l.storeError(w, err)
			return
		}
	}
	if kept == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	out := personJSON{ID: personID(id), Name: kept.Name, Biography: kept.Biography, Birthday: kept.Birthday,
		Deathday: kept.Deathday, Birthplace: kept.Birthplace, Department: kept.Department, Credits: []personCreditJSON{}}
	if img := images.ArtworkID("profile", kept.Profile); img != "" {
		out.Image = &library.ImageRef{URL: "/api/v1/images/" + img}
	}
	if out.Credits, err = l.personCredits(ctx, p, kept.Credits); err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, out)
}

// personCredits are a person's titles the profile may see, each once with
// every role, those a library has first, then the latest first.
func (l Library) personCredits(ctx context.Context, p *store.Profile, credits []store.PersonCredit) ([]personCreditJSON, error) {
	var keys []string
	roles := map[string][]string{}
	for _, c := range credits {
		k := c.Kind + "/tmdb/" + c.TMDB
		if _, seen := roles[k]; !seen {
			keys = append(keys, k)
			roles[k] = nil
		}
		if c.Role != "" && !slices.Contains(roles[k], c.Role) {
			roles[k] = append(roles[k], c.Role)
		}
	}
	titles, err := l.titleSummaries(ctx, p, keys)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(titles, func(a, b library.TitleSummary) int {
		ah, bh := a.Availability != "missing", b.Availability != "missing"
		if ah != bh {
			if ah {
				return -1
			}
			return 1
		}
		return b.Year - a.Year
	})
	out := make([]personCreditJSON, 0, len(titles))
	for _, t := range titles {
		ref, _ := titleRef(t.ID)
		out = append(out, personCreditJSON{Title: t, Roles: roles[ref.Key()]})
	}
	return out, nil
}
