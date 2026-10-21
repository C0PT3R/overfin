-- Each profile's watchlist: titles (movies, shows, seasons, episodes) its
-- user wants to watch. Private to the profile; deleted with it (the core
-- tells the module, see ForgetProfiles).
CREATE TABLE IF NOT EXISTS watchlist (
    profile_id TEXT NOT NULL,
    item_id    TEXT NOT NULL,           -- the API's item ID (item_…)
    added_at   INTEGER NOT NULL,
    PRIMARY KEY (profile_id, item_id)
) STRICT;
