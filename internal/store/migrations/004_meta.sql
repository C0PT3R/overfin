-- Facts about the database itself. "key-check" is derived from the secret
-- key (never the key itself), so Overfin can tell a wrong key at startup.
CREATE TABLE meta (
    name  TEXT PRIMARY KEY,
    value BLOB NOT NULL
) STRICT;
