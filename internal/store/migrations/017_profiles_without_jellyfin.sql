-- overfin: foreign keys off
-- Profiles no longer need a Jellyfin user of their own: Overfin's one
-- Jellyfin account serves everyone (milestone 4, step 4). Those that have
-- one keep it (to bring their history over once, or for a linked
-- account); new ones have none. SQLite can't drop NOT NULL, so the table
-- is rebuilt, with foreign keys off for the copy (the runner checks them
-- before committing), so nothing pointing at a profile is lost.
CREATE TABLE profiles_new (
    id                TEXT PRIMARY KEY,     -- prof_…
    account_id        TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('standard', 'kids')),
    owner             INTEGER NOT NULL CHECK (owner IN (0, 1)),
    jellyfin_user_id  TEXT UNIQUE,          -- NULL: none of its own
    jellyfin_username TEXT UNIQUE,
    jellyfin_token    BLOB,                 -- sealed, bound to this profile's id
    language          TEXT,
    pin_hash          BLOB,                 -- keyed hash, never the PIN
    pin_failures      INTEGER NOT NULL DEFAULT 0,
    pin_locked        INTEGER NOT NULL DEFAULT 0 CHECK (pin_locked IN (0, 1)),
    created_at        INTEGER NOT NULL,
    pin_failed_at     INTEGER,
    pin_locked_until  INTEGER,
    movie_ceiling     TEXT,
    series_ceiling    TEXT,
    libraries         TEXT,
    blocked_tags      TEXT,
    UNIQUE (account_id, name),
    CHECK ((jellyfin_user_id IS NULL) = (jellyfin_token IS NULL))
) STRICT;

INSERT INTO profiles_new (id, account_id, name, kind, owner, jellyfin_user_id, jellyfin_username,
    jellyfin_token, language, pin_hash, pin_failures, pin_locked, created_at, pin_failed_at,
    pin_locked_until, movie_ceiling, series_ceiling, libraries, blocked_tags)
SELECT id, account_id, name, kind, owner, jellyfin_user_id, jellyfin_username,
    jellyfin_token, language, pin_hash, pin_failures, pin_locked, created_at, pin_failed_at,
    pin_locked_until, movie_ceiling, series_ceiling, libraries, blocked_tags
FROM profiles;

DROP TABLE profiles;
ALTER TABLE profiles_new RENAME TO profiles;
CREATE UNIQUE INDEX profiles_one_owner ON profiles (account_id) WHERE owner = 1;
