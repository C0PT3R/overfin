-- What each profile has done with each title, kept by Overfin for every
-- title wherever it plays: where it stopped, whether it watched it, how
-- many times and when last, and whether it's a favorite. Keyed by the
-- title itself (its TMDB ID, an episode by its show's and its numbers),
-- not by Jellyfin's item, so the same film in two versions has one state.
-- Private to its profile, deleted with it (SECURITY.md rule 40).
CREATE TABLE profile_titles (
    profile_id     TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    title          TEXT NOT NULL,     -- e.g. movie/tmdb/949, series/tmdb/1399/s1/e2, item/<id>
    item_id        TEXT,              -- the library item it was last seen as
    position_ms    INTEGER NOT NULL DEFAULT 0,
    played         INTEGER NOT NULL DEFAULT 0 CHECK (played IN (0, 1)),
    play_count     INTEGER NOT NULL DEFAULT 0,
    last_played_at INTEGER,
    favorite       INTEGER NOT NULL DEFAULT 0 CHECK (favorite IN (0, 1)),
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (profile_id, title)
) STRICT;
CREATE INDEX profile_titles_recent ON profile_titles (profile_id, last_played_at);

-- Profiles whose data was brought over from Jellyfin once.
CREATE TABLE profile_imports (
    profile_id  TEXT PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,
    imported_at INTEGER NOT NULL
) STRICT;
