-- Series a profile took out of Continue Watching, and when: their next
-- episode stays out until the profile watches the series again.
CREATE TABLE continue_dismissed (
    profile_id   TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    series       TEXT NOT NULL,   -- the series' title key
    dismissed_at INTEGER NOT NULL,
    PRIMARY KEY (profile_id, series)
) STRICT;
