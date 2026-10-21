-- Each indexed item's title key (see TitleKeys), computed after each sync,
-- so what profiles did with titles joins with what the library has.
ALTER TABLE catalog_items ADD COLUMN title_key TEXT;
CREATE INDEX catalog_items_title_key ON catalog_items (title_key);
