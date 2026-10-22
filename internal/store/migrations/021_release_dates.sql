-- When each title came out, with its certifications: its first release
-- (festivals aside) and when it could first be watched at home (a film's
-- first digital, physical or TV release; a series' first air date), as
-- TMDB has them, YYYY-MM-DD. They decide which titles no library has are
-- shown (the panel's "titles shown" setting). Every title is fetched again
-- once, to fill them in.
ALTER TABLE title_ratings_fetched ADD COLUMN released TEXT;
ALTER TABLE title_ratings_fetched ADD COLUMN at_home TEXT;
DELETE FROM title_ratings_fetched;
