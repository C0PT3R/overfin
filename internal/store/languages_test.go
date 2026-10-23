package store

import (
	"context"
	"strings"
	"testing"
)

func TestLanguages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	// Never set: English (United States).
	if offered, def, set, err := s.Languages(ctx); err != nil || set || def != "en-US" || strings.Join(offered, ",") != "en-US" {
		t.Errorf("fresh: %v %q %v %v", offered, def, set, err)
	}
	// An install from before: its TMDB language in its ratings country.
	if err := s.SetTMDBLanguage(ctx, "fr"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRatingsCountry(ctx, "CA"); err != nil {
		t.Fatal(err)
	}
	if _, def, set, _ := s.Languages(ctx); set || def != "fr-CA" {
		t.Errorf("from before: %q %v", def, set)
	}

	// Saved: the default's language is TMDB's, and changing it says so.
	changed, err := s.SetLanguages(ctx, []string{"fr-CA", "en-US"}, "fr-CA")
	if err != nil || changed {
		t.Errorf("same language: %v %v", changed, err)
	}
	if offered, def, set, _ := s.Languages(ctx); !set || def != "fr-CA" || strings.Join(offered, ",") != "fr-CA,en-US" {
		t.Errorf("saved: %v %q %v", offered, def, set)
	}
	if changed, err := s.SetLanguages(ctx, []string{"fr-CA", "en-US"}, "en-US"); err != nil || !changed {
		t.Errorf("another default: %v %v", changed, err)
	}
	if l, _ := s.TMDBLanguage(ctx); l != "en" {
		t.Errorf("TMDB's language: %q", l)
	}
}
