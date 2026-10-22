-- Keys the operator gives other apps (Stewarr…) to read what Overfin
-- shares with them: totals per title, never anything per person
-- (SECURITY.md rule 40). Only a hash of each key is kept.
CREATE TABLE app_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    key_hash     BLOB NOT NULL UNIQUE,
    scope        TEXT NOT NULL CHECK (scope IN ('watch-stats')),
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
) STRICT;
