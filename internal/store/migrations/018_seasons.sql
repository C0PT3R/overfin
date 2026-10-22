-- TMDB's seasons and episodes, for every series Overfin keeps (in a library
-- or not): what each season and episode is called, says and looks like.
-- Like every TMDB datum, kept five months at most (fetched_at), and deleted
-- with TMDB's other data.
CREATE TABLE catalog_seasons (
    series     TEXT NOT NULL,      -- the series' TMDB ID
    season     INTEGER NOT NULL,
    name       TEXT NOT NULL,
    overview   TEXT NOT NULL,
    air_date   TEXT,               -- YYYY-MM-DD
    poster     TEXT,               -- a TMDB image path, checked
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (series, season)
) STRICT;

CREATE TABLE catalog_episodes (
    series   TEXT NOT NULL,
    season   INTEGER NOT NULL,
    episode  INTEGER NOT NULL,
    name     TEXT NOT NULL,
    overview TEXT NOT NULL,
    air_date TEXT,
    runtime  INTEGER,              -- minutes
    still    TEXT,
    PRIMARY KEY (series, season, episode),
    FOREIGN KEY (series, season) REFERENCES catalog_seasons (series, season) ON DELETE CASCADE
) STRICT;

-- Every library title is fetched again, with its whole page and seasons.
DELETE FROM artwork_fetched;
