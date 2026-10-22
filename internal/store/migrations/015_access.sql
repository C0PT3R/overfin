-- What a profile may see beyond its ratings: the libraries it gets and the
-- tags hidden from it, applied by Overfin (SECURITY.md rule 28). Both are
-- JSON arrays: libraries by Jellyfin ID, NULL for every library; tags as
-- Jellyfin has them, NULL for none.
ALTER TABLE profiles ADD COLUMN libraries TEXT;
ALTER TABLE profiles ADD COLUMN blocked_tags TEXT;

-- The index gets each title's tags, and who's in films and series (for a
-- person's titles). The next full pass fills them in: the sync starts over.
CREATE TABLE catalog_tags (
    item_id TEXT NOT NULL REFERENCES catalog_items(id) ON DELETE CASCADE,
    tag     TEXT NOT NULL,
    PRIMARY KEY (item_id, tag)
) STRICT;
CREATE INDEX catalog_tags_tag ON catalog_tags (tag COLLATE NOCASE);

CREATE TABLE catalog_people (
    item_id   TEXT NOT NULL REFERENCES catalog_items(id) ON DELETE CASCADE,
    person_id TEXT NOT NULL,   -- Jellyfin's person ID, 32 hex digits
    PRIMARY KEY (item_id, person_id)
) STRICT;
CREATE INDEX catalog_people_person ON catalog_people (person_id);

DELETE FROM meta WHERE name = 'catalog-state';
