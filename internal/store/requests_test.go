package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestRequests(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	if _, ok, err := s.SeerrUser(ctx, acct.ID); ok || err != nil {
		t.Fatalf("no Seerr user yet: %v %v", ok, err)
	}
	s.SetSeerrUser(ctx, acct.ID, 42, time.Now())
	if id, ok, _ := s.SeerrUser(ctx, acct.ID); !ok || id != 42 {
		t.Errorf("Seerr user: %d %v", id, ok)
	}

	r := Request{AccountID: acct.ID, ProfileID: prof.ID, Title: "series/tmdb/1399", Seasons: []int{2, 3}, SeerrID: 7,
		Status: "pending", CreatedAt: time.Now()}
	if err := s.AddRequest(ctx, &r); err != nil || r.ID == "" {
		t.Fatalf("add: %v %q", err, r.ID)
	}
	got, err := s.ProfileRequests(ctx, acct.ID, []string{prof.ID})
	if err != nil || len(got) != 1 || !slices.Equal(got[0].Seasons, []int{2, 3}) || got[0].SeerrID != 7 {
		t.Fatalf("profile's requests: %+v %v", got, err)
	}
	if byTitle, _ := s.AccountRequests(ctx, acct.ID, []string{"series/tmdb/1399", "movie/tmdb/1"}); len(byTitle) != 1 {
		t.Errorf("by title: %+v", byTitle)
	}
	s.SetRequestStatus(ctx, r.ID, "approved", time.Now())
	if one, err := s.RequestByID(ctx, acct.ID, r.ID); err != nil || one.Status != "approved" {
		t.Errorf("by ID: %+v %v", one, err)
	}
	// Another account's request is not found.
	other, _, _ := s.CreateAccount(ctx, "subject-2", owner("2"))
	if _, err := s.RequestByID(ctx, other.ID, r.ID); err != ErrNotFound {
		t.Errorf("another account's: %v", err)
	}
	s.DeleteRequest(ctx, acct.ID, r.ID)
	if got, _ := s.ProfileRequests(ctx, acct.ID, []string{prof.ID}); len(got) != 0 {
		t.Errorf("after deleting: %+v", got)
	}
}
