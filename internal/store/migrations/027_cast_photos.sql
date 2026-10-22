-- Which titles each cast photo belongs to, so a photo's access check finds
-- its titles at once instead of reading every title's cast. Kept in step
-- with catalog_titles.details by triggers.
CREATE TABLE cast_photos (
    path TEXT NOT NULL,
    key  TEXT NOT NULL,
    PRIMARY KEY (path, key)
) STRICT, WITHOUT ROWID;
CREATE INDEX cast_photos_key ON cast_photos (key);

INSERT OR IGNORE INTO cast_photos (path, key)
    SELECT json_extract(c.value, '$.profile'), t.kind || '/tmdb/' || t.tmdb
    FROM catalog_titles t, json_each(t.details, '$.cast') c
    WHERE t.details IS NOT NULL AND json_extract(c.value, '$.profile') IS NOT NULL
        AND json_extract(c.value, '$.profile') != '';

CREATE TRIGGER cast_photos_insert AFTER INSERT ON catalog_titles WHEN NEW.details IS NOT NULL BEGIN
    INSERT OR IGNORE INTO cast_photos (path, key)
        SELECT json_extract(c.value, '$.profile'), NEW.kind || '/tmdb/' || NEW.tmdb
        FROM json_each(NEW.details, '$.cast') c
        WHERE json_extract(c.value, '$.profile') IS NOT NULL AND json_extract(c.value, '$.profile') != '';
END;
CREATE TRIGGER cast_photos_update AFTER UPDATE OF details ON catalog_titles BEGIN
    DELETE FROM cast_photos WHERE key = OLD.kind || '/tmdb/' || OLD.tmdb;
    INSERT OR IGNORE INTO cast_photos (path, key)
        SELECT json_extract(c.value, '$.profile'), NEW.kind || '/tmdb/' || NEW.tmdb
        FROM json_each(NEW.details, '$.cast') c
        WHERE NEW.details IS NOT NULL
            AND json_extract(c.value, '$.profile') IS NOT NULL AND json_extract(c.value, '$.profile') != '';
END;
CREATE TRIGGER cast_photos_delete AFTER DELETE ON catalog_titles BEGIN
    DELETE FROM cast_photos WHERE key = OLD.kind || '/tmdb/' || OLD.tmdb;
END;

-- Stills are looked up by path too.
CREATE INDEX catalog_episodes_still ON catalog_episodes (still) WHERE still IS NOT NULL;
