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
		cast = append(cast, Person{Name: p.Name, Character: p.Character, Profile: p.ProfilePath})
	}
	for _, p := range raw.AggregateCredits.Cast {
		person := Person{Name: p.Name, Profile: p.ProfilePath}
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
