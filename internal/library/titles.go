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

// TitleSummary is a title wherever it's listed.
type TitleSummary struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // movie, series, season, episode, other
	Name string `json:"name"`
	Year int    `json:"year,omitempty"`
	// AirDate is a season's or an episode's, YYYY-MM-DD.
	AirDate    string `json:"airDate,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	// Availability: available, partiallyAvailable (a series or a season
	// with some of its episodes) or missing.
	Availability  string        `json:"availability"`
	Playable      bool          `json:"playable"`
	Series        *TitleParent  `json:"series,omitempty"`
	SeasonNumber  *int          `json:"seasonNumber,omitempty"`
	EpisodeNumber *int          `json:"episodeNumber,omitempty"`
	EpisodeCount  *EpisodeCount `json:"episodeCount,omitempty"`
	Images        *Images       `json:"images,omitempty"`
	UserState     *UserState    `json:"userState,omitempty"`
}

// TitlePrefix starts every title's ID.
const TitlePrefix = "title_"
