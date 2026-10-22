// Package seerr is Overfin's client for Seerr, for the catalog: what's
// trending, popular or upcoming, and a title's details, with TMDB's data.
//
// Every call is a typed method here (SECURITY.md rule 1), with the API key
// in a header, never in a URL. The client never follows redirects, bounds
// response sizes and times out (rule 6), checks every ID and image path it
// receives (rules 5 and 7), and keeps only plain text.
package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/textclean"
	"github.com/overfin/server/internal/tmdb"
)

// maxResponse bounds every response read from Seerr.
const maxResponse = 4 << 20

// ErrUnauthorized: Seerr refused the API key.
var ErrUnauthorized = tmdb.ErrUnauthorized

// Client talks to one Seerr server.
type Client struct {
	base *url.URL
	key  string
	http *http.Client
}

// New returns a client for the Seerr server at baseURL, with its API key.
func New(baseURL, key string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("seerr: the address must be a plain http:// or https:// address")
	}
	return &Client{base: u, key: key, http: &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

// get reads path under /api/v1, with its query encoded, never pasted into
// the path (rule 2).
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := *c.base
	u.Path += "/api/v1" + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("seerr: %s: %w", path, err)
	}
	defer res.Body.Close()
	body := io.LimitReader(res.Body, maxResponse)
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		io.Copy(io.Discard, body)
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		io.Copy(io.Discard, body)
		return ErrNotFound
	case res.StatusCode != http.StatusOK:
		io.Copy(io.Discard, body)
		return fmt.Errorf("seerr: %s answered %d", path, res.StatusCode)
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("seerr: %s: decoding the answer: %w", path, err)
	}
	return nil
}

// ErrNotFound: Seerr doesn't know the title.
var ErrNotFound = tmdb.ErrNotFound

// Check tells whether the server is Seerr and takes the key, and which
// version it runs.
func (c *Client) Check(ctx context.Context) (string, error) {
	var status struct{ Version string }
	if err := c.get(ctx, "/status", nil, &status); err != nil {
		return "", err
	}
	var me struct{ ID int }
	if err := c.get(ctx, "/auth/me", nil, &me); err != nil {
		return "", err
	}
	if me.ID == 0 {
		return "", ErrUnauthorized
	}
	return textclean.Plain(status.Version, 32), nil
}

// TMDB's data, as Seerr passes it on: Seerr is a tmdb.Source.
type (
	Title         = tmdb.Title
	Details       = tmdb.Details
	Person        = tmdb.Person
	Season        = tmdb.Season
	Page          = tmdb.Page
	Certification = tmdb.Certification
)

// Lists are the discovery lists Overfin offers.
var Lists = tmdb.Lists

// Discover returns one page (from 1) of a list, for films ("movie") or
// series ("series").
func (c *Client) Discover(ctx context.Context, list, kind string, page int) (*Page, error) {
	tv := map[string]string{"movie": "movies", "series": "tv"}[kind]
	if tv == "" || page < 1 || page > 500 {
		return nil, errors.New("seerr: invalid discovery query")
	}
	q := url.Values{"page": {strconv.Itoa(page)}}
	var path string
	switch list {
	case "trending":
		path = "/discover/trending"
		q.Set("mediaType", map[string]string{"movie": "movie", "series": "tv"}[kind])
	case "popular":
		path = "/discover/" + tv
	case "upcoming":
		path = "/discover/" + tv + "/upcoming"
	default:
		return nil, errors.New("seerr: invalid discovery list")
	}
	var raw struct {
		Page       int
		TotalPages int
		Results    []rawTitle
	}
	if err := c.get(ctx, path, q, &raw); err != nil {
		return nil, err
	}
	out := &Page{Page: raw.Page, TotalPages: min(raw.TotalPages, 500)}
	for _, r := range raw.Results {
		if t, ok := r.title(); ok && t.Kind == kind {
			out.Titles = append(out.Titles, t)
		}
	}
	return out, nil
}

// Details returns a title's page.
func (c *Client) Details(ctx context.Context, kind string, tmdbID int) (*Details, error) {
	path := map[string]string{"movie": "/movie/", "series": "/tv/"}[kind]
	if path == "" || !tmdb.ValidID(tmdbID) {
		return nil, ErrNotFound
	}
	var raw struct {
		rawTitle
		Genres  []struct{ Name string }
		Credits struct {
			Cast []struct{ Name, Character, ProfilePath string }
		}
		Seasons []struct {
			SeasonNumber int
			Name         string
			EpisodeCount int
		}
	}
	if err := c.get(ctx, path+strconv.Itoa(tmdbID), nil, &raw); err != nil {
		return nil, err
	}
	if raw.MediaType == "" {
		raw.MediaType = map[string]string{"movie": "movie", "series": "tv"}[kind]
	}
	t, ok := raw.title()
	if !ok || t.TMDBID != tmdbID || t.Kind != kind {
		return nil, ErrNotFound
	}
	var genres []string
	for _, g := range raw.Genres {
		genres = append(genres, g.Name)
	}
	var cast []Person
	for _, p := range raw.Credits.Cast {
		cast = append(cast, Person{Name: p.Name, Character: p.Character, Profile: p.ProfilePath})
	}
	var seasons []Season
	for _, se := range raw.Seasons {
		seasons = append(seasons, Season{Number: se.SeasonNumber, Name: se.Name, EpisodeCount: se.EpisodeCount})
	}
	return tmdb.MakeDetails(t, genres, cast, seasons), nil
}

// rawTitle is a title as Seerr sends it, films and series alike.
type rawTitle struct {
	ID           int
	MediaType    string
	Title        string // films
	Name         string // series
	ReleaseDate  string // films
	FirstAirDate string // series
	Overview     string
	PosterPath   string
	BackdropPath string
	MediaInfo    *struct{ Status int }
}

func (r rawTitle) title() (Title, bool) {
	kind := map[string]string{"movie": "movie", "tv": "series"}[r.MediaType]
	name, date := r.Title, r.ReleaseDate
	if kind == "series" {
		name, date = r.Name, r.FirstAirDate
	}
	status := 0
	if r.MediaInfo != nil {
		status = r.MediaInfo.Status
	}
	return tmdb.MakeTitle(r.ID, kind, name, date, r.Overview, r.PosterPath, r.BackdropPath, status)
}

// Certifications returns the scales for films ("movie") or series
// ("series"), by country.
func (c *Client) Certifications(ctx context.Context, kind string) (map[string][]Certification, error) {
	path := map[string]string{"movie": "/certifications/movie", "series": "/certifications/tv"}[kind]
	if path == "" {
		return nil, errors.New("seerr: invalid kind")
	}
	var raw struct {
		Certifications map[string][]struct {
			Certification string
			Meaning       string
			Order         int
		}
	}
	if err := c.get(ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	scales := map[string][]tmdb.Scale{}
	for cc, levels := range raw.Certifications {
		for _, l := range levels {
			scales[cc] = append(scales[cc], tmdb.Scale{Certification: l.Certification, Meaning: l.Meaning, Order: l.Order})
		}
	}
	return tmdb.MakeScales(scales), nil
}

// Ratings returns a title's certifications, by country: a film's release
// certifications (each of its releases may have its own), a series'
// content rating.
func (c *Client) Ratings(ctx context.Context, kind string, tmdbID int) (map[string][]string, error) {
	path := map[string]string{"movie": "/movie/", "series": "/tv/"}[kind]
	if path == "" || !tmdb.ValidID(tmdbID) {
		return nil, ErrNotFound
	}
	var raw struct {
		ID       int
		Releases struct {
			Results []struct {
				Country      string `json:"iso_3166_1"`
				ReleaseDates []struct {
					Certification string
				} `json:"release_dates"`
			}
		}
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string
			}
		}
	}
	if err := c.get(ctx, path+strconv.Itoa(tmdbID), nil, &raw); err != nil {
		return nil, err
	}
	if raw.ID != tmdbID {
		return nil, ErrNotFound
	}
	out := tmdb.Ratings{}
	for _, r := range raw.Releases.Results {
		for _, d := range r.ReleaseDates {
			out.Add(r.Country, d.Certification)
		}
	}
	for _, r := range raw.ContentRatings.Results {
		out.Add(r.Country, r.Rating)
	}
	return out, nil
}
