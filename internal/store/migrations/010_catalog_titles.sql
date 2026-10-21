-- The catalog's titles from TMDB (through Seerr): every one Overfin has
-- come across, in the library or not, so discovery is answered from here.
-- TMDB's terms allow keeping its data six months at most: seen_at is when
-- it was last fetched, and rows not fetched again for five months are
-- deleted. Disconnecting Seerr deletes them all.
CREATE TABLE catalog_titles (
    kind       TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb       TEXT NOT NULL,
    name       TEXT NOT NULL,
    year       INTEGER,
    overview   TEXT NOT NULL,
    poster     TEXT,               -- TMDB image paths, checked
    backdrop   TEXT,
    status     INTEGER NOT NULL,   -- Seerr's media status
    details    TEXT,               -- JSON: genres, cast, seasons; NULL until opened
    seen_at    INTEGER NOT NULL,
    details_at INTEGER,
    PRIMARY KEY (kind, tmdb)
) STRICT;

-- Discovery's lists, as ordered TMDB IDs, refreshed in the background.
CREATE TABLE catalog_lists (
    list     TEXT NOT NULL,        -- trending, popular, upcoming
    kind     TEXT NOT NULL,
    position INTEGER NOT NULL,
    tmdb     TEXT NOT NULL,
    PRIMARY KEY (list, kind, position)
) STRICT;
