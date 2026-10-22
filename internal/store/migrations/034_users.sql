-- Users who sign in with Overfin itself (identity mode "managed"): their
-- email, and a password if they set one. Their subject is what accounts and
-- logins refer to, as an identity provider's user ID would be.
CREATE TABLE users (
    subject       TEXT PRIMARY KEY,              -- user_…
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT,                          -- package password's form; NULL: none set
    admin         INTEGER NOT NULL DEFAULT 0 CHECK (admin IN (0, 1)),
    disabled_at   INTEGER,
    created_at    INTEGER NOT NULL
) STRICT;

-- Sign-in codes sent by email: the code, typed in the browser that asked
-- for it, or the link in the same email. Only keyed hashes are kept.
CREATE TABLE signin_codes (
    id         TEXT PRIMARY KEY,
    email      TEXT NOT NULL COLLATE NOCASE,
    code_hash  BLOB NOT NULL,
    link_hash  BLOB NOT NULL UNIQUE,
    binding    BLOB NOT NULL,                    -- hash of the asking browser's cookie
    purpose    TEXT NOT NULL CHECK (purpose IN ('signin', 'fresh')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX signin_codes_by_email ON signin_codes (email);
