-- Identification (the control panel): telling misidentified titles, and
-- fixing them by hand.
--
-- What the index keeps for telling them: the last names of where each
-- film and series is (never the whole path, SECURITY.md rule 55), a film's
-- length, and the TMDB ID Jellyfin gave it, apart from the one used.
ALTER TABLE catalog_items ADD COLUMN folder_name TEXT;
ALTER TABLE catalog_items ADD COLUMN file_name TEXT;
ALTER TABLE catalog_items ADD COLUMN runtime_ms INTEGER;
ALTER TABLE catalog_items ADD COLUMN jellyfin_tmdb TEXT;
UPDATE catalog_items SET jellyfin_tmdb = tmdb;

-- The TMDB title an admin chose for a film or a series, over Jellyfin's.
-- Overfin keeps it: Jellyfin's scans can't undo it. It goes with the item.
CREATE TABLE title_overrides (
    item_id TEXT PRIMARY KEY REFERENCES catalog_items(id) ON DELETE CASCADE,
    tmdb    TEXT NOT NULL,
    set_at  INTEGER NOT NULL
) STRICT;

-- The library is read again whole, for the names, and every library title
-- fetched again from TMDB, for its original title and language.
DELETE FROM meta WHERE name = 'catalog-state';
DELETE FROM artwork_fetched;
