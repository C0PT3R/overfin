package tmdb

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

	"github.com/overfin/server/internal/textclean"
	"github.com/overfin/server/internal/upstream"
)

// ProjectToken is the Overfin project's own TMDB key (a read access
// token), shipped with Overfin as Jellyfin and Seerr ship theirs: each
// installation calls TMDB itself, for its own users, and operators never
// need a key of their own. TMDB's terms apply: non-commercial use, its data
// kept six months at most, TMDB credited.
const ProjectToken = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiIwMDllNzNiYzVlMTFiNjFhZTdlMTUzYjc4MDViN2Y5NiIsIm5iZiI6MTc5MjYzNTE4Mi45MzgsInN1YiI6IjZhZDk3MTJlYmVlYmQ5ZTgxYjcyOGFjNiIsInNjb3BlcyI6WyJhcGlfcmVhZCJdLCJ2ZXJzaW9uIjoxfQ._SHeos1lMVK5BpOIjmLLHlLX3r7AL-j_ODM7mYs7YOc"

// BaseURL is TMDB's API.
const BaseURL = "https://api.themoviedb.org/3"

// maxResponse bounds every response read from TMDB.
const maxResponse = 4 << 20

// Client talks to TMDB. Every call is a typed method (SECURITY.md rule
// 1), the key in a header, never in a URL; it never follows redirects,
// bounds response sizes and times out (rule 6).
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
	// Language, when set, is the language TMDB answers in, ISO 639-1 or
	// regional ("fr", "fr-CA": TMDB falls back to the language's other
	// text field by field): titles, overviews, and the text on posters
	// and logos.
	Language func() string
}

// language is the language to ask for; "" for TMDB's default (English).
func (c *Client) language() string {
	if c.Language == nil {
		return ""
	}
	if l := c.Language(); languageCode.MatchString(l) {
		return l
	}
	return ""
}

// languageCode is what an ISO 639-1 code, regional or not, looks like.
var languageCode = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

// withLanguage adds the language to a query.
func (c *Client) withLanguage(q url.Values) url.Values {
	if q == nil {
		q = url.Values{}
	}
	if l := c.language(); l != "" {
		q.Set("language", l)
	}
	return q
}

// New returns a client for TMDB at baseURL (BaseURL, or a test server),
// with a read access token.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || token == "" {
		return nil, errors.New("tmdb: invalid address or key")
	}
	return &Client{base: u, token: token, http: &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

// get reads path, with its query encoded, never pasted into the path
// (rule 2).
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := *c.base
	u.Path += path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb: %s: %w", path, upstream.Plain(err))
	}
	defer res.Body.Close()
	body := io.LimitReader(res.Body, maxResponse)
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		io.Copy(io.Discard, body)
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		io.Copy(io.Discard, body)
		return ErrNotFound
	case res.StatusCode != http.StatusOK:
		io.Copy(io.Discard, body)
		return fmt.Errorf("tmdb: %s answered %d", path, res.StatusCode)
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("tmdb: %s: decoding the answer: %w", path, err)
	}
	return nil
}

// rawTitle is a title as TMDB sends it, films and series alike.
type rawTitle struct {
	ID           int    `json:"id"`
	MediaType    string `json:"media_type"`
	Title        string `json:"title"` // films
	Name         string `json:"name"`  // series
	ReleaseDate  string `json:"release_date"`
	FirstAirDate string `json:"first_air_date"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
	// OriginalTitle is a film's, OriginalName a series'.
	OriginalTitle    string `json:"original_title"`
	OriginalName     string `json:"original_name"`
	OriginalLanguage string `json:"original_language"`
}

func (r rawTitle) title(kind string) (Title, bool) {
	t, ok := MakeTitle(r.ID, kind, r.Title, r.ReleaseDate, r.Overview, r.PosterPath, r.BackdropPath, 0)
	original := r.OriginalTitle
	if kind == "series" {
		t, ok = MakeTitle(r.ID, kind, r.Name, r.FirstAirDate, r.Overview, r.PosterPath, r.BackdropPath, 0)
		original = r.OriginalName
	}
	t.OriginalName = textclean.Plain(original, 300)
	if isoLanguage.MatchString(r.OriginalLanguage) {
		t.Language = r.OriginalLanguage
	}
	return t, ok
}

// tv is TMDB's word for a kind in paths.
func tv(kind string) string { return map[string]string{"movie": "movie", "series": "tv"}[kind] }

func (c *Client) Discover(ctx context.Context, list, kind string, page int) (*Page, error) {
	k := tv(kind)
	if k == "" || page < 1 || page > 500 {
		return nil, errors.New("tmdb: invalid discovery query")
	}
	var path string
	switch list {
	case "trending":
		path = "/trending/" + k + "/week"
	case "popular":
		path = "/" + k + "/popular"
	case "upcoming":
		path = "/movie/upcoming"
		if kind == "series" {
			path = "/tv/on_the_air"
		}
	default:
		return nil, errors.New("tmdb: invalid discovery list")
	}
	var raw struct {
		Page       int        `json:"page"`
		TotalPages int        `json:"total_pages"`
		Results    []rawTitle `json:"results"`
	}
	if err := c.get(ctx, path, c.withLanguage(url.Values{"page": {strconv.Itoa(page)}}), &raw); err != nil {
		return nil, err
	}
	out := &Page{Page: raw.Page, TotalPages: min(raw.TotalPages, 500)}
	for _, r := range raw.Results {
		if r.MediaType != "" && r.MediaType != k {
			continue
		}
		if t, ok := r.title(kind); ok {
			out.Titles = append(out.Titles, t)
		}
	}
	return out, nil
}

// rawDetails is a title's page as TMDB sends it, with its credits and its
// certifications appended.
type rawDetails struct {
	rawTitle
	Genres []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Credits struct {
		Cast []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			Character   string `json:"character"`
			ProfilePath string `json:"profile_path"`
		} `json:"cast"`
	} `json:"credits"`
	Seasons []struct {
		SeasonNumber int    `json:"season_number"`
		Name         string `json:"name"`
		EpisodeCount int    `json:"episode_count"`
	} `json:"seasons"`
	ReleaseDates struct {
		Results []struct {
			Country      string `json:"iso_3166_1"`
			ReleaseDates []struct {
				Certification string `json:"certification"`
				Type          int    `json:"type"`
				ReleaseDate   string `json:"release_date"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
	ContentRatings struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"content_ratings"`
}

func (c *Client) details(ctx context.Context, kind string, tmdbID int) (*rawDetails, error) {
	k := tv(kind)
	if k == "" || !ValidID(tmdbID) {
		return nil, ErrNotFound
	}
	extra := "credits,release_dates"
	if kind == "series" {
		extra = "credits,content_ratings"
	}
	var raw rawDetails
	if err := c.get(ctx, "/"+k+"/"+strconv.Itoa(tmdbID), c.withLanguage(url.Values{"append_to_response": {extra}}), &raw); err != nil {
		return nil, err
	}
	if raw.ID != tmdbID {
		return nil, ErrNotFound
	}
	return &raw, nil
}

func (c *Client) Details(ctx context.Context, kind string, tmdbID int) (*Details, error) {
	raw, err := c.details(ctx, kind, tmdbID)
	if err != nil {
		return nil, err
	}
	t, ok := raw.title(kind)
	if !ok {
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
	for _, s := range raw.Seasons {
		seasons = append(seasons, Season{Number: s.SeasonNumber, Name: s.Name, EpisodeCount: s.EpisodeCount})
	}
	return MakeDetails(t, genres, cast, seasons), nil
}

func (c *Client) Release(ctx context.Context, kind string, tmdbID int) (*Release, error) {
	raw, err := c.details(ctx, kind, tmdbID)
	if err != nil {
		return nil, err
	}
	out := &Release{Ratings: Ratings{}}
	for _, r := range raw.ReleaseDates.Results {
		for _, d := range r.ReleaseDates {
			out.Ratings.Add(r.Country, d.Certification)
			out.AddDate(d.Type, d.ReleaseDate)
		}
	}
	for _, r := range raw.ContentRatings.Results {
		out.Ratings.Add(r.Country, r.Rating)
	}
	if kind == "series" {
		out.Released = date(raw.FirstAirDate)
		out.AtHome = out.Released
	}
	return out, nil
}

func (c *Client) Certifications(ctx context.Context, kind string) (map[string][]Certification, error) {
	k := tv(kind)
	if k == "" {
		return nil, errors.New("tmdb: invalid kind")
	}
	var raw struct {
		Certifications map[string][]struct {
			Certification string `json:"certification"`
			Meaning       string `json:"meaning"`
			Order         int    `json:"order"`
		} `json:"certifications"`
	}
	if err := c.get(ctx, "/certification/"+k+"/list", nil, &raw); err != nil {
		return nil, err
	}
	scales := map[string][]Scale{}
	for cc, levels := range raw.Certifications {
		for _, l := range levels {
			scales[cc] = append(scales[cc], Scale{Certification: l.Certification, Meaning: l.Meaning, Order: l.Order})
		}
	}
	return MakeScales(scales), nil
}

// Artwork is a title's images, as TMDB paths (checked; "" when there's
// none): its poster and backdrop, and its logo (the title as art), in the
// client's language when TMDB has them.
type Artwork struct {
	Poster   string
	Backdrop string
	Logo     string
}

// rawImage is one of TMDB's images of a title.
type rawImage struct {
	FilePath string `json:"file_path"`
	Language string `json:"iso_639_1"`
}

// pick chooses, among images (best first, as TMDB orders them), one in the
// language, else one without text, else one in English.
func pick(images []rawImage, lang string) string {
	for _, want := range []string{lang, "", "en"} {
		for _, im := range images {
			if im.Language == want && imagePath.MatchString(im.FilePath) {
				return im.FilePath
			}
		}
	}
	return ""
}

// isoLanguage is an ISO 639-1 code, as TMDB writes them.
var isoLanguage = regexp.MustCompile(`^[a-z]{2}$`)
