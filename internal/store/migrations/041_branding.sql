-- The operator's branding files: their site's logo, and its banners for
-- dark and light pages, shown instead of Overfin's when branding is on
-- (meta 'branding' keeps the switch and the name).
CREATE TABLE branding_files (
	kind TEXT PRIMARY KEY CHECK (kind IN ('logo', 'banner-dark', 'banner-light')),
	content_type TEXT NOT NULL,
	data BLOB NOT NULL,
	updated_at INTEGER NOT NULL
);
