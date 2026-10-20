-- How many times each profile said hello. Gone with the profile.
CREATE TABLE hello_visits (
    profile_id TEXT PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,
    visits     INTEGER NOT NULL
) STRICT;
