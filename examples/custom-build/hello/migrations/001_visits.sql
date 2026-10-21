-- How many times each profile said hello. Gone with the profile (the core
-- tells the module, see ForgetProfiles).
CREATE TABLE hello_visits (
    profile_id TEXT PRIMARY KEY,
    visits     INTEGER NOT NULL
) STRICT;
