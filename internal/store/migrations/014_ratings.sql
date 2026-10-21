-- Overfin's own content ratings, from TMDB (through Seerr): the scales of
-- certifications, per country and per kind, and each title's
-- certifications, in every country TMDB has. A kids profile sees a title
-- only when every certification it has in the server's ratings country is
-- within the profile's ceiling for its kind; anything else is hidden
-- (SECURITY.md rule 28). Like every TMDB datum, they're kept five months at
-- most and deleted with Seerr's connection.
CREATE TABLE rating_scales (
    kind          TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    country       TEXT NOT NULL,     -- ISO 3166-1, e.g. FR
    certification TEXT NOT NULL,     -- e.g. 12, PG-13, TV-Y7
    meaning       TEXT NOT NULL,
    position      INTEGER NOT NULL,  -- TMDB's order: higher is more mature
    PRIMARY KEY (kind, country, certification)
) STRICT;

CREATE TABLE title_ratings (
    kind          TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb          TEXT NOT NULL,
    country       TEXT NOT NULL,
    certification TEXT NOT NULL,
    PRIMARY KEY (kind, tmdb, country, certification)
) STRICT;

-- When each title's certifications were last fetched, found or not.
CREATE TABLE title_ratings_fetched (
    kind       TEXT NOT NULL,
    tmdb       TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (kind, tmdb)
) STRICT;

-- A kids profile's ceilings, one for films and one for series, on the
-- ratings country's scales. Jellyfin's rating labels mean nothing here:
-- every kids profile sees nothing until its owner picks new ceilings.
ALTER TABLE profiles DROP COLUMN rating_ceiling;
ALTER TABLE profiles ADD COLUMN movie_ceiling TEXT;
ALTER TABLE profiles ADD COLUMN series_ceiling TEXT;
