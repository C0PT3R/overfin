-- Titles' texts in the languages offered besides the default (whose texts
-- stay with the titles, seasons and episodes themselves): what accounts in
-- those languages read. Fetched from TMDB when first needed, and slowly for
-- the whole library.
CREATE TABLE catalog_title_texts (
    kind       TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb       TEXT NOT NULL,
    language   TEXT NOT NULL,     -- a regional tag, e.g. en-CA
    name       TEXT NOT NULL,
    overview   TEXT NOT NULL,
    details    TEXT NOT NULL,     -- JSON, as catalog_titles.details
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (kind, tmdb, language)
) STRICT;

CREATE TABLE catalog_season_texts (
    series     TEXT NOT NULL,     -- the series' TMDB ID
    season     INTEGER NOT NULL,
    language   TEXT NOT NULL,
    name       TEXT NOT NULL,
    overview   TEXT NOT NULL,
    PRIMARY KEY (series, season, language)
) STRICT;

CREATE TABLE catalog_episode_texts (
    series     TEXT NOT NULL,
    season     INTEGER NOT NULL,
    episode    INTEGER NOT NULL,
    language   TEXT NOT NULL,
    name       TEXT NOT NULL,
    overview   TEXT NOT NULL,
    PRIMARY KEY (series, season, episode, language)
) STRICT;
