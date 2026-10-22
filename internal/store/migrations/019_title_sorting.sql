-- What lists sort and filter titles by, from TMDB like everything shown: a
-- name to sort by (lower case, without accents or a leading article), the
-- release (or first air) date, and TMDB's users' rating. Every library
-- title is fetched again once, to fill them in.
ALTER TABLE catalog_titles ADD COLUMN sort_name TEXT;
ALTER TABLE catalog_titles ADD COLUMN release_date TEXT;  -- YYYY-MM-DD
ALTER TABLE catalog_titles ADD COLUMN rating REAL;
UPDATE catalog_titles SET sort_name = lower(name);
DELETE FROM artwork_fetched;
