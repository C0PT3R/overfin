-- The libraries, as the index last read them (Jellyfin's view of each, as
-- JSON, in Jellyfin's order), so listing them doesn't ask Jellyfin.
CREATE TABLE catalog_libraries (
    id       TEXT PRIMARY KEY,
    position INTEGER NOT NULL,
    item     TEXT NOT NULL
) STRICT;
