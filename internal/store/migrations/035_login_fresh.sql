-- When a login was last confirmed (a password or code typed again), for the
-- control panel on the site's address: admins confirm before going in,
-- whether or not they have an account with a session.
ALTER TABLE logins ADD COLUMN fresh_at INTEGER;
