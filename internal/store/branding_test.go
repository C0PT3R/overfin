package store

import (
	"context"
	"testing"
	"time"
)

func TestBranding(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if b, err := s.Branding(ctx); err != nil || b.On || b.Name != "" || len(b.Files) != 0 {
		t.Fatalf("before: %+v %v", b, err)
	}
	if err := s.SetBranding(ctx, true, "Riverside"); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := s.SetBrandingFile(ctx, "banner-dark", "image/svg+xml", []byte("<svg/>"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBrandingFile(ctx, "banner-dark", "image/png", []byte("png"), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBrandingFile(ctx, "other", "image/png", []byte("png"), now); err == nil {
		t.Error("an unknown kind was kept")
	}
	b, err := s.Branding(ctx)
	if err != nil || !b.On || b.Name != "Riverside" || len(b.Files) != 1 || !b.Files["banner-dark"].Equal(now.Add(time.Hour)) {
		t.Errorf("after: %+v %v", b, err)
	}
	if f, ok, err := s.BrandingFileOf(ctx, "banner-dark"); err != nil || !ok || f.ContentType != "image/png" || string(f.Data) != "png" {
		t.Errorf("file: %+v %v %v", f, ok, err)
	}
	s.DeleteBrandingFile(ctx, "banner-dark")
	if _, ok, err := s.BrandingFileOf(ctx, "banner-dark"); ok || err != nil {
		t.Errorf("deleted: %v %v", ok, err)
	}
}
