-- How many of TMDB's users its rating comes from, so a score from a handful
-- isn't shown like one from thousands. Every library title is fetched
-- again once, to fill it in.
ALTER TABLE catalog_titles ADD COLUMN rating_votes INTEGER;
DELETE FROM artwork_fetched;
