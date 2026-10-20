-- Account templates: what an account may do. Accounts follow theirs live:
-- editing a template changes every account on it. Operators create the
-- templates they want; the built-in Default is minimal, and every account
-- without another template is on it.
CREATE TABLE account_templates (
    id           TEXT PRIMARY KEY,   -- tmpl_…; Default is tmpl_default
    name         TEXT NOT NULL UNIQUE,
    max_streams  INTEGER NOT NULL CHECK (max_streams BETWEEN 1 AND 100),
    max_profiles INTEGER NOT NULL CHECK (max_profiles BETWEEN 1 AND 20),
    created_at   INTEGER NOT NULL
) STRICT;

INSERT INTO account_templates (id, name, max_streams, max_profiles, created_at)
VALUES ('tmpl_default', 'Default', 1, 1, strftime('%s', 'now'));

-- NULL: the Default template. accounts.max_streams (per-account limits) is
-- no longer used: limits come from templates.
ALTER TABLE accounts ADD COLUMN template_id TEXT REFERENCES account_templates(id);
