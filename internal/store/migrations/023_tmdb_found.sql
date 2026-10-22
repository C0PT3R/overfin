-- Films and series the library knows only by an IMDb or TVDB ID, found on
-- TMDB: what TMDB said each ID names (NULL: nothing), so the index gets
-- the title's TMDB ID, and keeps it across syncs.
CREATE TABLE tmdb_found (
    kind       TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    source     TEXT NOT NULL CHECK (source IN ('imdb', 'tvdb')),
    external   TEXT NOT NULL,
    tmdb       TEXT,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (kind, source, external)
) STRICT;
