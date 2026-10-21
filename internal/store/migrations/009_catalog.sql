-- The catalog's index: every title of the library, as Overfin's Jellyfin
-- service account sees it. Nothing personal, no images and no URLs: posters
-- are fetched when asked for, from the item's ID and its image tags.
CREATE TABLE catalog_items (
    id             TEXT PRIMARY KEY,   -- Jellyfin's item ID, 32 hex digits
    kind           TEXT NOT NULL CHECK (kind IN ('movie', 'series', 'season', 'episode')),
    name           TEXT NOT NULL,
    sort_name      TEXT NOT NULL,
    year           INTEGER,
    series_id      TEXT,               -- seasons and episodes
    season_id      TEXT,               -- episodes
    season_number  INTEGER,
    episode_number INTEGER,
    tmdb           TEXT,
    imdb           TEXT,
    tvdb           TEXT,
    rating         TEXT NOT NULL,      -- as Jellyfin has it; '' without one
    primary_tag    TEXT,
    backdrop_tag   TEXT,
    thumb_tag      TEXT,
    seen           INTEGER NOT NULL    -- the full pass that last saw it
) STRICT;

CREATE INDEX catalog_items_tmdb ON catalog_items (kind, tmdb) WHERE tmdb IS NOT NULL;
CREATE INDEX catalog_items_imdb ON catalog_items (imdb) WHERE imdb IS NOT NULL;
CREATE INDEX catalog_items_tvdb ON catalog_items (kind, tvdb) WHERE tvdb IS NOT NULL;
CREATE INDEX catalog_items_series ON catalog_items (series_id) WHERE series_id IS NOT NULL;
