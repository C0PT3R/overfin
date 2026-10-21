-- What library listings need from the index: which library a title is in,
-- when it was added and released, its rating score, its genres and
-- studios. The next full pass fills them in: the sync starts over.
ALTER TABLE catalog_items ADD COLUMN library_id TEXT;
ALTER TABLE catalog_items ADD COLUMN added_at INTEGER;
ALTER TABLE catalog_items ADD COLUMN premiere_date TEXT;  -- YYYY-MM-DD
ALTER TABLE catalog_items ADD COLUMN community_rating REAL;

CREATE TABLE catalog_genres (
    item_id TEXT NOT NULL REFERENCES catalog_items(id) ON DELETE CASCADE,
    genre   TEXT NOT NULL,
    PRIMARY KEY (item_id, genre)
) STRICT;
CREATE INDEX catalog_genres_genre ON catalog_genres (genre);

CREATE TABLE catalog_studios (
    item_id TEXT NOT NULL REFERENCES catalog_items(id) ON DELETE CASCADE,
    studio  TEXT NOT NULL,
    PRIMARY KEY (item_id, studio)
) STRICT;
CREATE INDEX catalog_studios_studio ON catalog_studios (studio);

CREATE INDEX catalog_items_library ON catalog_items (library_id, kind);

DELETE FROM meta WHERE name = 'catalog-state';
