-- Accounts on another Jellyfin server whose history was copied here
-- (migration, SECURITY.md rule 60): each into one Overfin account only.
-- Nothing else about them is kept.
CREATE TABLE history_copies (
    server_id  TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    copied_at  INTEGER NOT NULL,
    PRIMARY KEY (server_id, user_id)
) STRICT;
