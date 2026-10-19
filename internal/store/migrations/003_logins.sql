-- Logins made through Overfin's own OpenID Connect login (identity mode
-- "oidc"): who the identity provider says the visitor is. Sessions, per
-- account, sit on top as before.
CREATE TABLE logins (
    id             TEXT PRIMARY KEY,         -- login_…
    token_hash     BLOB NOT NULL UNIQUE,     -- SHA-256 of the cookie value
    subject        TEXT NOT NULL,            -- the identity provider's user ID
    groups         TEXT NOT NULL DEFAULT '', -- only when a group is required
    refresh_token  BLOB NOT NULL,            -- sealed, bound to this row
    checked_at     INTEGER NOT NULL,         -- last confirmation by the provider
    created_at     INTEGER NOT NULL,
    last_active_at INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL
) STRICT;

CREATE INDEX logins_by_subject ON logins (subject);
