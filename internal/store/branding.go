package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Branding is the operator's own branding: when On, their site's name,
// logo and banners replace Overfin's wherever people look (not in the
// control panel).
type Branding struct {
	On   bool   `json:"on"`
	Name string `json:"name"`
	// Files are the kinds uploaded ("logo", "banner-dark", "banner-light")
	// and when each was.
	Files map[string]time.Time `json:"-"`
}

// BrandingFile is an uploaded file, kept as it was sent.
type BrandingFile struct {
	ContentType string
	Data        []byte
	Updated     time.Time
}

// BrandingKinds are the files branding may have.
var BrandingKinds = []string{"logo", "banner-dark", "banner-light"}

// Branding reads the operator's branding; off, with no name, until set.
func (s *Store) Branding(ctx context.Context) (Branding, error) {
	var b Branding
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'branding'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &b); err != nil {
			return b, err
		}
	}
	b.Files = map[string]time.Time{}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, updated_at FROM branding_files`)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var at int64
		if err := rows.Scan(&kind, &at); err != nil {
			return b, err
		}
		b.Files[kind] = time.Unix(at, 0).UTC()
	}
	return b, rows.Err()
}

// SetBranding switches the operator's branding on or off, with its name.
func (s *Store) SetBranding(ctx context.Context, on bool, name string) error {
	raw, err := json.Marshal(Branding{On: on, Name: name})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('branding', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, raw)
	return err
}

// BrandingFileOf reads one branding file; ok is false without it.
func (s *Store) BrandingFileOf(ctx context.Context, kind string) (f BrandingFile, ok bool, err error) {
	var at int64
	err = s.db.QueryRowContext(ctx, `SELECT content_type, data, updated_at FROM branding_files WHERE kind = ?`, kind).
		Scan(&f.ContentType, &f.Data, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	f.Updated = time.Unix(at, 0).UTC()
	return f, err == nil, err
}

// SetBrandingFile keeps a branding file, replacing any of its kind.
func (s *Store) SetBrandingFile(ctx context.Context, kind, contentType string, data []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO branding_files (kind, content_type, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (kind) DO UPDATE SET content_type = excluded.content_type, data = excluded.data, updated_at = excluded.updated_at`,
		kind, contentType, data, now.Unix())
	return err
}

// DeleteBrandingFile removes a branding file.
func (s *Store) DeleteBrandingFile(ctx context.Context, kind string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM branding_files WHERE kind = ?`, kind)
	return err
}
