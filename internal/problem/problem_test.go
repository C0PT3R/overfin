package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSend(t *testing.T) {
	rec := httptest.NewRecorder()
	Send(rec, OwnerOnly, "")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var p Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Type != OwnerOnly || p.Status != 403 || p.Title == "" {
		t.Errorf("unexpected problem: %+v", p)
	}
}

func TestUnknownTypeBecomesInternal(t *testing.T) {
	p := New(Type("made-up"), "secret internal detail")
	if p.Type != Internal || p.Detail != "" {
		t.Errorf("got %+v, want a bare internal error", p)
	}
}

func TestEveryTypeIsRegistered(t *testing.T) {
	for _, typ := range []Type{NotFound, NotAuthenticated, OwnerOnly, AccountNotAllowed, AccountSetupRequired, AccountExists, ClaimFailed, ClaimNotAllowed, CrossSiteRequest, NotPlayable, NameTaken, ProfileLimitReached, StreamLimitReached, ServerBusy, PlaybackTakenOver, PlaybackExpired, PlaybackStopped, UnsupportedMediaType, InvalidRequest, InvalidCursor, RateLimited, Internal} {
		if _, ok := registry[typ]; !ok {
			t.Errorf("%s has no status and title", typ)
		}
	}
}
