-- People, from TMDB, kept when someone opens their page: who they are, and
-- the films and series they're credited in (JSON: kind, tmdb, role, crew;
-- the titles themselves are in catalog_titles). Like every TMDB datum,
-- kept five months at most. Every library title is fetched again once, so
-- its cast carries their TMDB IDs.
CREATE TABLE tmdb_people (
    id         TEXT PRIMARY KEY,   -- TMDB's
    name       TEXT NOT NULL,
    biography  TEXT NOT NULL,
    birthday   TEXT,
    deathday   TEXT,
    birthplace TEXT NOT NULL,
    department TEXT NOT NULL,
    profile    TEXT,               -- a TMDB image path, checked
    credits    TEXT NOT NULL,
    fetched_at INTEGER NOT NULL
) STRICT;
CREATE INDEX tmdb_people_profile ON tmdb_people (profile) WHERE profile IS NOT NULL;
DELETE FROM artwork_fetched;
