-- Signing in with an email and a password only. Codes by email no longer
-- sign anyone in: links by email now confirm a new person's address, or
-- let someone choose a new password.
DROP TABLE signin_codes;

-- Links sent by email: only a keyed hash of each is kept, used once.
CREATE TABLE email_links (
    id         TEXT PRIMARY KEY,
    subject    TEXT NOT NULL REFERENCES users (subject) ON DELETE CASCADE,
    link_hash  BLOB NOT NULL UNIQUE,
    purpose    TEXT NOT NULL CHECK (purpose IN ('verify', 'reset')),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX email_links_by_subject ON email_links (subject);

-- Whether a person confirmed their email (those who registered
-- themselves, until they do); whether they must choose a new password at
-- their next sign-in (accounts an admin made); and the invitation they
-- registered with, sealed, until their account picks it up.
ALTER TABLE users ADD COLUMN confirmed INTEGER NOT NULL DEFAULT 1 CHECK (confirmed IN (0, 1));
ALTER TABLE users ADD COLUMN must_change INTEGER NOT NULL DEFAULT 0 CHECK (must_change IN (0, 1));
ALTER TABLE users ADD COLUMN invite_code BLOB;
