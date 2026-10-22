-- The sign-up link the identity provider made for an invitation (its ID
-- there; the link itself isn't kept), given again while it works.
ALTER TABLE invites ADD COLUMN signup_token_id TEXT NOT NULL DEFAULT '';
