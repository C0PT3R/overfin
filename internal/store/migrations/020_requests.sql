-- Requests, made in Seerr on behalf of accounts.
--
-- Each account has a Seerr user of its own, anonymous (an opaque name, a
-- placeholder address): the link is kept here, and remade if Seerr loses
-- the user. It goes with the account.
CREATE TABLE seerr_users (
    account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    seerr_id   INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- Each request, by the profile that made it: the title (a film or a
-- series, by its key), the seasons asked for (JSON; NULL for a film or for
-- every season), Seerr's request, and its status as last read from Seerr.
-- Private to the account, deleted with the profile.
CREATE TABLE requests (
    id         TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    seasons    TEXT,
    seerr_id   INTEGER NOT NULL,
    status     TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX requests_account ON requests (account_id, title);
CREATE INDEX requests_profile ON requests (profile_id, created_at);
