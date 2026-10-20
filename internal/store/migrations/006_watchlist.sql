-- Each profile's watchlist: titles (movies, shows, seasons, episodes) its
-- user wants to watch. Private to the profile; deleted with it.
CREATE TABLE watchlist (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    item_id    TEXT NOT NULL,           -- the Jellyfin item, normalized
    added_at   INTEGER NOT NULL,
    PRIMARY KEY (profile_id, item_id)
) STRICT;
