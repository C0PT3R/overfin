-- Which kept people have their whole page: a search keeps only their name
-- and photo, and opening them must still fetch the rest. Those kept with
-- credits had their page fetched.
ALTER TABLE tmdb_people ADD COLUMN page INTEGER NOT NULL DEFAULT 0;
UPDATE tmdb_people SET page = 1 WHERE credits != '[]';
