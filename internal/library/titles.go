package library

// Titles, whether a library has them or not (milestone 5), as the API
// shows them.

// TitleParent is the series a season or an episode is of.
type TitleParent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// EpisodeCount is how much of a series or a season the library has.
type EpisodeCount struct {
	InLibrary int `json:"inLibrary"`
	Aired     int `json:"aired"`
}

// Rating is a title's users' score at one source.
type Rating struct {
	// Source is where it comes from: "tmdb".
	Source string `json:"source"`
	// Value is out of 10, to one decimal; Votes, how many people's.
	Value float64 `json:"value"`
	Votes int     `json:"votes"`
}

// TitleSummary is a title wherever it's listed.
type TitleSummary struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // movie, series, season, episode, other
	Name string `json:"name"`
	Year int    `json:"year,omitempty"`
	// AirDate is a season's or an episode's, YYYY-MM-DD.
	AirDate    string `json:"airDate,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	// Certification is its rating in the server's ratings country (its
	// series', for a season or an episode).
	Certification string `json:"certification,omitempty"`
	// Ratings are its users' scores, by source (TMDB's, for now): a film's
	// or a series'.
	Ratings []Rating `json:"ratings,omitempty"`
	// Availability: available, partiallyAvailable (a series or a season
	// with some of its episodes) or missing.
	Availability string       `json:"availability"`
	Playable     bool         `json:"playable"`
	Series       *TitleParent `json:"series,omitempty"`
	// Season is an episode's.
	Season        *TitleParent  `json:"season,omitempty"`
	SeasonNumber  *int          `json:"seasonNumber,omitempty"`
	EpisodeNumber *int          `json:"episodeNumber,omitempty"`
	EpisodeCount  *EpisodeCount `json:"episodeCount,omitempty"`
	Images        *Images       `json:"images,omitempty"`
	UserState     *UserState    `json:"userState,omitempty"`
	// Request is the account's latest request covering it, if any.
	Request *TitleRequest `json:"request,omitempty"`
}

// TitleRequest is a request covering a title: its ID and status.
type TitleRequest struct {
	// ID is the request's when the account made it (it may cancel it);
	// absent for someone else's.
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
}

// TitlePrefix starts every title's ID.
const TitlePrefix = "title_"
