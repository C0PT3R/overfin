-- Counting a series' episodes the libraries have reads only that series'
-- episodes (a range of title keys), not every episode.
CREATE INDEX catalog_items_kind_key ON catalog_items (kind, title_key);
