-- Accounts, profiles and sessions. Times are Unix seconds, UTC.

CREATE TABLE accounts (
    id          TEXT PRIMARY KEY,           -- acct_…
    subject     TEXT NOT NULL UNIQUE,       -- the identity provider's user ID
    created_at  INTEGER NOT NULL,
    disabled_at INTEGER                     -- set when the identity is disabled
) STRICT;

CREATE TABLE profiles (
    id                TEXT PRIMARY KEY,     -- prof_…
    account_id        TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('standard', 'kids')),
    owner             INTEGER NOT NULL CHECK (owner IN (0, 1)),
    jellyfin_user_id  TEXT NOT NULL UNIQUE,
    jellyfin_username TEXT NOT NULL UNIQUE, -- opaque, e.g. of-7f3k2q…
    jellyfin_token    BLOB NOT NULL,        -- sealed, bound to this profile's id
    language          TEXT,
    rating_ceiling    TEXT,
    pin_hash          BLOB,                 -- keyed hash, never the PIN
    pin_failures      INTEGER NOT NULL DEFAULT 0,
    pin_locked        INTEGER NOT NULL DEFAULT 0 CHECK (pin_locked IN (0, 1)),
    created_at        INTEGER NOT NULL,
    UNIQUE (account_id, name)
) STRICT;

-- Exactly one owner profile per account, enforced by the database.
CREATE UNIQUE INDEX profiles_one_owner ON profiles (account_id) WHERE owner = 1;

CREATE TABLE sessions (
    id                TEXT PRIMARY KEY,     -- sess_…, shown in session lists
    token_hash        BLOB NOT NULL UNIQUE, -- SHA-256 of the cookie value
    account_id        TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    active_profile_id TEXT REFERENCES profiles(id) ON DELETE SET NULL,
    device            TEXT,
    created_at        INTEGER NOT NULL,
    last_active_at    INTEGER NOT NULL,
    fresh_login_at    INTEGER,              -- last confirmed login, for step-up
    expires_at        INTEGER NOT NULL
) STRICT;

CREATE INDEX sessions_by_account ON sessions (account_id);
