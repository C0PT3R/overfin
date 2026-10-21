-- Invite templates: what an invitation made from one gives, as the operator
-- sets it up.
CREATE TABLE invites_templates (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    account_template TEXT NOT NULL,      -- the account template's ID
    access_days      INTEGER NOT NULL,   -- how long the account's access lasts; 0: no end
    valid_days       INTEGER NOT NULL,   -- how long the link works
    max_uses         INTEGER NOT NULL
) STRICT;

-- Invitations. Each copies its template's values when made, so changing a
-- template never changes the invitations already sent.
CREATE TABLE invites (
    id               TEXT PRIMARY KEY,
    code_hash        TEXT NOT NULL UNIQUE, -- SHA-256 of the link's code; the code itself is never kept
    label            TEXT NOT NULL,        -- who it's for, in the operator's words
    account_template TEXT NOT NULL,
    access_days      INTEGER NOT NULL,
    max_uses         INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    created_at       INTEGER NOT NULL
) STRICT;

-- Each use of an invitation. A use stays counted when its account is
-- deleted (the core tells the module, which forgets the account), so a used
-- single-use link never works again.
CREATE TABLE invites_uses (
    invite_id  TEXT NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
    account_id TEXT,
    used_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX invites_uses_invite ON invites_uses (invite_id);
