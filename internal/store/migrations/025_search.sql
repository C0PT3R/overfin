-- What titles are searched by: their name in lower case without accents
-- (filled in as titles are kept, and once for those already kept).
ALTER TABLE catalog_titles ADD COLUMN search_name TEXT;
CREATE INDEX catalog_items_kind_tmdb ON catalog_items (kind, tmdb);
