// Package tmdb is Overfin's access to TMDB's data: titles, their details
// and certifications, each country's scales of certifications, and
// discovery's lists. It comes from TMDB itself, with the Overfin
// project's own key (Client), or through Seerr (package seerr); both are
// a Source, and the rest of Overfin doesn't know which it uses.
//
// Everything here is checked and in plain text: IDs, image paths (rule
// 5), names and certifications.
package tmdb

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/textclean"
)

// Source is where TMDB's data comes from.
type Source interface {
	// Discover returns one page (from 1) of a list (Lists), for films
	// ("movie") or series ("series").
	Discover(ctx context.Context, list, kind string, page int) (*Page, error)
	// Details returns a title's page.
	Details(ctx context.Context, kind string, tmdbID int) (*Details, error)
	// Certifications returns the scales for films or series, by country.
	Certifications(ctx context.Context, kind string) (map[string][]Certification, error)
	// Ratings returns a title's certifications, by country.
	Ratings(ctx context.Context, kind string, tmdbID int) (map[string][]string, error)
}

var (
	// ErrNotFound: TMDB doesn't know the title.
	ErrNotFound = errors.New("tmdb: not found")
	// ErrUnauthorized: the key is refused.
	ErrUnauthorized = errors.New("tmdb: the key is refused")
)

// Lists are the discovery lists Overfin offers.
var Lists = []string{"trending", "popular", "upcoming"}

// Title is a title, checked and in plain text.
type Title struct {
	TMDBID   int
	Kind     string // movie, series
	Name     string
	Year     int
	Overview string
	// Poster and Backdrop are TMDB image paths, such as "/abc123.jpg":
	// checked, and fetched only from TMDB's fixed image host.
	Poster   string
	Backdrop string
	// Status is Seerr's media status: 0 when it has none (always, from
	// TMDB itself), 4 partly available, 5 available.
	Status int
}

// Details is a title's page: the title, and what lists don't carry.
type Details struct {
	Title
	Genres  []string
	Cast    []Person
	Seasons []Season
}

type Person struct {
	Name      string
	Character string
}

type Season struct {
	Number       int
	Name         string
	EpisodeCount int
}

// Page is one page of a discovery list.
type Page struct {
	Titles     []Title
	Page       int
	TotalPages int
}

// Certification is one level of a country's scale.
type Certification struct {
	Certification string
	Meaning       string
	// Order is TMDB's: higher is more mature.
	Order int
}

// ValidID says whether a TMDB ID can be one.
func ValidID(id int) bool { return id > 0 && id <= 9_999_999_999 }

// imagePath is what a TMDB image path may look like (rule 5).
var imagePath = regexp.MustCompile(`^/[A-Za-z0-9]{8,64}\.(jpg|png)$`)

// MakeTitle checks and cleans a title as TMDB (or Seerr) gives it: kind is
// "movie" or "series", date its release or first air date. ok is false
// when it isn't a title.
func MakeTitle(id int, kind, name, date, overview, poster, backdrop string, status int) (Title, bool) {
	if (kind != "movie" && kind != "series") || !ValidID(id) {
		return Title{}, false
	}
	t := Title{TMDBID: id, Kind: kind, Name: textclean.Plain(name, 300), Overview: textclean.Plain(overview, 2000)}
	if t.Name == "" {
		return Title{}, false
	}
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil && y > 1800 && y < 3000 {
			t.Year = y
		}
	}
	if imagePath.MatchString(poster) {
		t.Poster = poster
	}
	if imagePath.MatchString(backdrop) {
		t.Backdrop = backdrop
	}
	if status >= 0 && status <= 6 {
		t.Status = status
	}
	return t, true
}

// MakeDetails checks and cleans a title's genres, cast and seasons.
func MakeDetails(t Title, genres []string, cast []Person, seasons []Season) *Details {
	d := &Details{Title: t}
	for _, g := range genres {
		if name := textclean.Plain(g, 60); name != "" && len(d.Genres) < 20 {
			d.Genres = append(d.Genres, name)
		}
	}
	for _, p := range cast {
		if len(d.Cast) == 30 {
			break
		}
		if name := textclean.Plain(p.Name, 120); name != "" {
			d.Cast = append(d.Cast, Person{Name: name, Character: textclean.Plain(p.Character, 120)})
		}
	}
	for _, s := range seasons {
		if s.Number < 0 || s.Number > 1000 || s.EpisodeCount < 0 || s.EpisodeCount > 10_000 {
			continue
		}
		d.Seasons = append(d.Seasons, Season{Number: s.Number, Name: textclean.Plain(s.Name, 120), EpisodeCount: s.EpisodeCount})
	}
	return d
}

// country is what an ISO 3166-1 country code looks like.
var country = regexp.MustCompile(`^[A-Z]{2}$`)

// certification checks a certification as TMDB writes it, such as "PG-13",
// "12" or "TV-Y7"; "" when it isn't one.
func certification(s string) string {
	if s = strings.TrimSpace(s); len(s) > 16 || textclean.Plain(s, 16) != s {
		return ""
	}
	return s
}

// Scale is one raw level of a scale, before it's checked.
type Scale struct {
	Certification string
	Meaning       string
	Order         int
}

// MakeScales checks the scales TMDB (or Seerr) gives, by country.
func MakeScales(raw map[string][]Scale) map[string][]Certification {
	out := map[string][]Certification{}
	for cc, levels := range raw {
		if !country.MatchString(cc) || len(levels) > 50 {
			continue
		}
		for _, l := range levels {
			if cert := certification(l.Certification); cert != "" && l.Order >= 0 && l.Order <= 100 {
				out[cc] = append(out[cc], Certification{Certification: cert, Meaning: textclean.Plain(l.Meaning, 1000), Order: l.Order})
			}
		}
	}
	return out
}

// Ratings collects a title's certifications, by country, checked.
type Ratings map[string][]string

// Add adds a certification for a country, once.
func (r Ratings) Add(cc, cert string) {
	if cert = certification(cert); cert == "" || !country.MatchString(cc) || len(r[cc]) >= 20 {
		return
	}
	for _, c := range r[cc] {
		if c == cert {
			return
		}
	}
	r[cc] = append(r[cc], cert)
}
