package tmdb

import (
	"context"
	"net/url"
	"regexp"
	"strconv"

	"github.com/overfin/server/internal/textclean"
)

// Full is everything Overfin keeps about a film or a series, from one call:
// its page, its runtime and tagline, and its images.
type Full struct {
	Details
	// Runtime is in minutes: a film's, or a series' usual episode's; 0
	// when TMDB doesn't say.
	Runtime int
	Tagline string
	// ReleaseDate is a film's release or a series' first air date,
	// YYYY-MM-DD, or "".
	ReleaseDate string
	// Rating is TMDB's users' average, 0 to 10; 0 when no one rated it.
	Rating  float64
	Artwork Artwork
}

// imageLanguages is which images to ask for: those in the language, those
// without text, and English ones.
func imageLanguages(lang string) string {
	if lang != "" && lang != "en" {
		return lang + ",null,en"
	}
	return "null,en"
}

// Title returns everything about a film ("movie") or a series ("series"),
// in the client's language.
func (c *Client) Title(ctx context.Context, kind string, tmdbID int) (*Full, error) {
	k := tv(kind)
	if k == "" || !ValidID(tmdbID) {
		return nil, ErrNotFound
	}
	lang := c.language()
	var raw struct {
		rawDetails
		Runtime        int     `json:"runtime"`
		EpisodeRunTime []int   `json:"episode_run_time"`
		Tagline        string  `json:"tagline"`
		VoteAverage    float64 `json:"vote_average"`
		// A series' cast over all its seasons (credits has its latest
		// season's only).
		AggregateCredits struct {
			Cast []struct {
				ID          int    `json:"id"`
				Name        string `json:"name"`
				ProfilePath string `json:"profile_path"`
				Roles       []struct {
					Character string `json:"character"`
				} `json:"roles"`
			} `json:"cast"`
		} `json:"aggregate_credits"`
		Images struct {
			Logos   []rawImage `json:"logos"`
			Posters []rawImage `json:"posters"`
		} `json:"images"`
	}
	extra := "credits,images"
	if kind == "series" {
		extra = "aggregate_credits,images"
	}
	q := c.withLanguage(url.Values{"append_to_response": {extra}, "include_image_language": {imageLanguages(lang)}})
	if err := c.get(ctx, "/"+k+"/"+strconv.Itoa(tmdbID), q, &raw); err != nil {
		return nil, err
	}
	if raw.ID != tmdbID {
		return nil, ErrNotFound
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
		cast = append(cast, Person{ID: p.ID, Name: p.Name, Character: p.Character, Profile: p.ProfilePath})
	}
	for _, p := range raw.AggregateCredits.Cast {
		person := Person{ID: p.ID, Name: p.Name, Profile: p.ProfilePath}
		if len(p.Roles) > 0 {
			person.Character = p.Roles[0].Character // the role they played most
		}
		cast = append(cast, person)
	}
	var seasons []Season
	for _, s := range raw.Seasons {
		seasons = append(seasons, Season{Number: s.SeasonNumber, Name: s.Name, EpisodeCount: s.EpisodeCount})
	}
	out := &Full{Details: *MakeDetails(t, genres, cast, seasons), Tagline: textclean.Plain(raw.Tagline, 300)}
	out.ReleaseDate = date(raw.ReleaseDate)
	if kind == "series" {
		out.ReleaseDate = date(raw.FirstAirDate)
	}
	if raw.VoteAverage > 0 && raw.VoteAverage <= 10 {
		out.Rating = raw.VoteAverage
	}
	runtime := raw.Runtime
	if kind == "series" && len(raw.EpisodeRunTime) > 0 {
		runtime = raw.EpisodeRunTime[0]
	}
	if runtime > 0 && runtime < 10_000 {
		out.Runtime = runtime
	}
	// The poster TMDB picks for the language, else the best one listed.
	out.Artwork = Artwork{Poster: t.Poster, Backdrop: t.Backdrop, Logo: pick(raw.Images.Logos, lang)}
	if out.Artwork.Poster == "" {
		out.Artwork.Poster = pick(raw.Images.Posters, lang)
	}
	return out, nil
}

// SeasonFull is a season of a series: its name, overview, air date and
// poster, and its episodes.
type SeasonFull struct {
	Number   int
	Name     string
	Overview string
	AirDate  string // YYYY-MM-DD, or ""
	Poster   string
	Episodes []Episode
}

// Episode is one episode of a season, in plain text.
type Episode struct {
	Number   int
	Name     string
	Overview string
	AirDate  string // YYYY-MM-DD, or ""
	Runtime  int    // minutes, 0 when TMDB doesn't say
	Still    string
}

// dateOnly is what a TMDB date looks like.
var dateOnly = regexp.MustCompile(`^[12][0-9]{3}-[01][0-9]-[0-3][0-9]$`)

func date(s string) string {
	if dateOnly.MatchString(s) {
		return s
	}
	return ""
}

// Season returns one season of a series, with its episodes, in the
// client's language.
func (c *Client) Season(ctx context.Context, seriesID, season int) (*SeasonFull, error) {
	if !ValidID(seriesID) || season < 0 || season > 1000 {
		return nil, ErrNotFound
	}
	var raw struct {
		SeasonNumber *int   `json:"season_number"`
		Name         string `json:"name"`
		Overview     string `json:"overview"`
		AirDate      string `json:"air_date"`
		PosterPath   string `json:"poster_path"`
		Episodes     []struct {
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			Overview      string `json:"overview"`
			AirDate       string `json:"air_date"`
			Runtime       int    `json:"runtime"`
			StillPath     string `json:"still_path"`
		} `json:"episodes"`
	}
	path := "/tv/" + strconv.Itoa(seriesID) + "/season/" + strconv.Itoa(season)
	if err := c.get(ctx, path, c.withLanguage(nil), &raw); err != nil {
		return nil, err
	}
	if raw.SeasonNumber != nil && *raw.SeasonNumber != season {
		return nil, ErrNotFound
	}
	out := &SeasonFull{Number: season, Name: textclean.Plain(raw.Name, 120), Overview: textclean.Plain(raw.Overview, 2000),
		AirDate: date(raw.AirDate)}
	if imagePath.MatchString(raw.PosterPath) {
		out.Poster = raw.PosterPath
	}
	seen := map[int]bool{}
	for _, e := range raw.Episodes {
		if e.EpisodeNumber < 0 || e.EpisodeNumber > 10_000 || seen[e.EpisodeNumber] || len(out.Episodes) == 2000 {
			continue
		}
		seen[e.EpisodeNumber] = true
		ep := Episode{Number: e.EpisodeNumber, Name: textclean.Plain(e.Name, 300), Overview: textclean.Plain(e.Overview, 2000),
			AirDate: date(e.AirDate)}
		if e.Runtime > 0 && e.Runtime < 10_000 {
			ep.Runtime = e.Runtime
		}
		if imagePath.MatchString(e.StillPath) {
			ep.Still = e.StillPath
		}
		out.Episodes = append(out.Episodes, ep)
	}
	return out, nil
}

// Find returns the TMDB ID of the film ("movie") or the series ("series")
// an IMDb ("imdb") or TVDB ("tvdb") ID names; ErrNotFound when TMDB knows
// none.
func (c *Client) Find(ctx context.Context, kind, source, id string) (int, error) {
	param := map[string]string{"imdb": "imdb_id", "tvdb": "tvdb_id"}[source]
	if param == "" || !externalID.MatchString(id) || (kind != "movie" && kind != "series") {
		return 0, ErrNotFound
	}
	var raw struct {
		MovieResults []struct{ ID int } `json:"movie_results"`
		TVResults    []struct{ ID int } `json:"tv_results"`
	}
	if err := c.get(ctx, "/find/"+id, url.Values{"external_source": {param}}, &raw); err != nil {
		return 0, err
	}
	results := raw.MovieResults
	if kind == "series" {
		results = raw.TVResults
	}
	if len(results) == 0 || !ValidID(results[0].ID) {
		return 0, ErrNotFound
	}
	return results[0].ID, nil
}

// externalID is what an IMDb or a TVDB ID looks like.
var externalID = regexp.MustCompile(`^(tt)?[0-9]{1,12}$`)

// PersonFull is a person, as TMDB has them: who they are, and the films
// and series they're credited in.
type PersonFull struct {
	ID         int
	Name       string
	Biography  string
	Birthday   string // YYYY-MM-DD, or ""
	Deathday   string
	Birthplace string
	Profile    string // a TMDB image path, or ""
	// Department is what they're known for: Acting, Directing…
	Department string
	Credits    []Credit
}

// Credit is one of a person's films or series, and what they did in it.
type Credit struct {
	Title Title
	// Role is their character, or their job behind the camera.
	Role string
	// Crew: a job behind the camera, not a part.
	Crew bool
}

// Person returns a person, with their films' and series' credits, in the
// client's language.
func (c *Client) Person(ctx context.Context, id int) (*PersonFull, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	type credit struct {
		rawTitle
		Character string `json:"character"`
		Job       string `json:"job"`
	}
	var raw struct {
		ID                 int    `json:"id"`
		Name               string `json:"name"`
		Biography          string `json:"biography"`
		Birthday           string `json:"birthday"`
		Deathday           string `json:"deathday"`
		PlaceOfBirth       string `json:"place_of_birth"`
		ProfilePath        string `json:"profile_path"`
		KnownForDepartment string `json:"known_for_department"`
		CombinedCredits    struct {
			Cast []credit `json:"cast"`
			Crew []credit `json:"crew"`
		} `json:"combined_credits"`
	}
	q := c.withLanguage(url.Values{"append_to_response": {"combined_credits"}})
	if err := c.get(ctx, "/person/"+strconv.Itoa(id), q, &raw); err != nil {
		return nil, err
	}
	if raw.ID != id {
		return nil, ErrNotFound
	}
	out := &PersonFull{ID: id, Name: textclean.Plain(raw.Name, 300), Biography: textclean.Plain(raw.Biography, 5000),
		Birthday: date(raw.Birthday), Deathday: date(raw.Deathday), Birthplace: textclean.Plain(raw.PlaceOfBirth, 200),
		Department: textclean.Plain(raw.KnownForDepartment, 60)}
	if out.Name == "" {
		return nil, ErrNotFound
	}
	if imagePath.MatchString(raw.ProfilePath) {
		out.Profile = raw.ProfilePath
	}
	add := func(cr credit, crew bool) {
		kind := map[string]string{"movie": "movie", "tv": "series"}[cr.MediaType]
		if kind == "" || len(out.Credits) == maxCredits {
			return
		}
		t, ok := cr.title(kind)
		if !ok {
			return
		}
		role := cr.Character
		if crew {
			role = cr.Job
		}
		out.Credits = append(out.Credits, Credit{Title: t, Role: textclean.Plain(role, 120), Crew: crew})
	}
	for _, cr := range raw.CombinedCredits.Cast {
		add(cr, false)
	}
	for _, cr := range raw.CombinedCredits.Crew {
		add(cr, true)
	}
	return out, nil
}

// maxCredits bounds a person's credits.
const maxCredits = 500

// PersonHit is a person a search found.
type PersonHit struct {
	ID         int
	Name       string
	Profile    string // a TMDB image path, or ""
	Department string
	// First: TMDB ranked them above every title.
	First bool
}

// Search finds films, series and people whose names match query, in
// TMDB's order of relevance, in the client's language (its first page).
func (c *Client) Search(ctx context.Context, query string) ([]Title, []PersonHit, error) {
	var raw struct {
		Results []struct {
			rawTitle
			ProfilePath        string `json:"profile_path"`
			KnownForDepartment string `json:"known_for_department"`
		} `json:"results"`
	}
	q := c.withLanguage(url.Values{"query": {query}, "include_adult": {"false"}})
	if err := c.get(ctx, "/search/multi", q, &raw); err != nil {
		return nil, nil, err
	}
	var titles []Title
	var people []PersonHit
	for i, r := range raw.Results {
		switch r.MediaType {
		case "movie", "tv":
			kind := map[string]string{"movie": "movie", "tv": "series"}[r.MediaType]
			if t, ok := r.title(kind); ok {
				titles = append(titles, t)
			}
		case "person":
			p := PersonHit{ID: r.ID, Name: textclean.Plain(r.Name, 300), Department: textclean.Plain(r.KnownForDepartment, 60),
				First: i == 0}
			if imagePath.MatchString(r.ProfilePath) {
				p.Profile = r.ProfilePath
			}
			if ValidID(p.ID) && p.Name != "" {
				people = append(people, p)
			}
		}
	}
	return titles, people, nil
}
