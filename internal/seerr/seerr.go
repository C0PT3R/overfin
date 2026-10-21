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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/C0PT3R/overfin-server/internal/textclean"
)

// maxResponse bounds every response read from Seerr.
const maxResponse = 4 << 20

// ErrUnauthorized: Seerr refused the API key.
var ErrUnauthorized = errors.New("seerr: the API key is refused")

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
var ErrNotFound = errors.New("seerr: not found")

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

// Title is a title as Seerr gives it, checked and in plain text.
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
	// Status is Seerr's media status: 0 when it has none, 4 partly
	// available, 5 available.
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

// Lists are the discovery lists Overfin offers.
var Lists = []string{"trending", "popular", "upcoming"}

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
	if path == "" || tmdbID <= 0 || tmdbID > 9_999_999_999 {
		return nil, ErrNotFound
	}
	var raw struct {
		rawTitle
		Genres  []struct{ Name string }
		Credits struct {
			Cast []struct{ Name, Character string }
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
	d := &Details{Title: t}
	for _, g := range raw.Genres {
		if name := textclean.Plain(g.Name, 60); name != "" && len(d.Genres) < 20 {
			d.Genres = append(d.Genres, name)
		}
	}
	for _, p := range raw.Credits.Cast {
		if len(d.Cast) == 30 {
			break
		}
		if name := textclean.Plain(p.Name, 120); name != "" {
			d.Cast = append(d.Cast, Person{Name: name, Character: textclean.Plain(p.Character, 120)})
		}
	}
	for _, s := range raw.Seasons {
		if s.SeasonNumber < 0 || s.SeasonNumber > 1000 || s.EpisodeCount < 0 || s.EpisodeCount > 10_000 {
			continue
		}
		d.Seasons = append(d.Seasons, Season{Number: s.SeasonNumber, Name: textclean.Plain(s.Name, 120), EpisodeCount: s.EpisodeCount})
	}
	return d, nil
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

// imagePath is what a TMDB image path may look like (rule 5).
var imagePath = regexp.MustCompile(`^/[A-Za-z0-9]{8,64}\.(jpg|png)$`)

func (r rawTitle) title() (Title, bool) {
	kind := map[string]string{"movie": "movie", "tv": "series"}[r.MediaType]
	if kind == "" || r.ID <= 0 || r.ID > 9_999_999_999 {
		return Title{}, false
	}
	name, date := r.Title, r.ReleaseDate
	if kind == "series" {
		name, date = r.Name, r.FirstAirDate
	}
	t := Title{TMDBID: r.ID, Kind: kind, Name: textclean.Plain(name, 300), Overview: textclean.Plain(r.Overview, 2000)}
	if t.Name == "" {
		return Title{}, false
	}
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil && y > 1800 && y < 3000 {
			t.Year = y
		}
	}
	if imagePath.MatchString(r.PosterPath) {
		t.Poster = r.PosterPath
	}
	if imagePath.MatchString(r.BackdropPath) {
		t.Backdrop = r.BackdropPath
	}
	if r.MediaInfo != nil && r.MediaInfo.Status >= 0 && r.MediaInfo.Status <= 6 {
		t.Status = r.MediaInfo.Status
	}
	return t, true
}
