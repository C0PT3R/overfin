-- Overfin's own artwork, from TMDB: each title's images, by title key
-- (movie/tmdb/949, series/tmdb/1399, series/tmdb/1399/s1,
-- series/tmdb/1399/s1/e2), as TMDB image paths. Like every TMDB datum,
-- kept five months at most, deleted with TMDB's other data.
CREATE TABLE artwork (
    key  TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('poster', 'backdrop', 'logo', 'still')),
    path TEXT NOT NULL,
    PRIMARY KEY (key, type)
) STRICT;
CREATE INDEX artwork_path ON artwork (path);

-- When each film's or series' artwork (a series' with its seasons') was
-- last fetched, found or not.
CREATE TABLE artwork_fetched (
    key        TEXT PRIMARY KEY,  -- movie/tmdb/949, series/tmdb/1399
    fetched_at INTEGER NOT NULL
) STRICT;
