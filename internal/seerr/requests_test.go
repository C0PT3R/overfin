package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestRequests(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/user":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":42}`)
		case "GET /api/v1/user/42":
			io.WriteString(w, `{"id":42}`)
		case "POST /api/v1/request":
			switch body["mediaId"].(float64) {
			case 1:
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"message":"Movie Quota exceeded."}`)
			case 2:
				w.WriteHeader(http.StatusConflict)
			default:
				w.WriteHeader(http.StatusCreated)
				io.WriteString(w, `{"id":7,"status":1,"type":"tv","media":{"tmdbId":1399,"status":2},"seasons":[{"seasonNumber":2}]}`)
			}
		case "GET /api/v1/request":
			if r.URL.Query().Get("requestedBy") != "42" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"results":[{"id":7,"status":2,"type":"tv","media":{"tmdbId":1399,"status":5}},
				{"id":8,"status":3,"type":"movie","media":{"tmdbId":949,"status":1}}]}`)
		case "DELETE /api/v1/request/7":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "key")
	ctx := context.Background()

	id, err := c.CreateUser(ctx, "overfin-abc", "abc@overfin.invalid", "secret")
	if err != nil || id != 42 || bodies[0]["password"] != "secret" || bodies[0]["email"] != "abc@overfin.invalid" {
		t.Fatalf("create user: %d %v %v", id, err, bodies[0])
	}
	if ok, err := c.UserExists(ctx, 42); !ok || err != nil {
		t.Errorf("user 42: %v %v", ok, err)
	}
	if ok, err := c.UserExists(ctx, 43); ok || err != nil {
		t.Errorf("user 43: %v %v", ok, err)
	}

	r, err := c.RequestTitle(ctx, 42, "series", 1399, []int{2})
	last := bodies[len(bodies)-1]
	if err != nil || r.ID != 7 || r.Status != 1 || !slices.Equal(r.Seasons, []int{2}) ||
		last["mediaType"] != "tv" || last["userId"] != float64(42) || last["ignoreQuota"] != nil {
		t.Errorf("request: %+v %v %v", r, err, last)
	}
	if _, err := c.RequestTitle(ctx, 42, "series", 1399, nil); err != nil || bodies[len(bodies)-1]["seasons"] != "all" {
		t.Errorf("every season: %v %v", err, bodies[len(bodies)-1])
	}
	if _, err := c.RequestTitle(ctx, 42, "movie", 1, nil); !errors.Is(err, ErrQuota) {
		t.Errorf("quota: %v", err)
	}
	if _, err := c.RequestTitle(ctx, 42, "movie", 2, nil); !errors.Is(err, ErrExists) {
		t.Errorf("already requested: %v", err)
	}

	all, err := c.UserRequests(ctx, 42)
	if err != nil || len(all) != 2 || all[0].MediaStatus != 5 || all[1].Kind != "movie" || all[1].Status != 3 {
		t.Errorf("requests: %+v %v", all, err)
	}
	if err := c.CancelRequest(ctx, 7); err != nil {
		t.Errorf("cancel: %v", err)
	}
	if err := c.CancelRequest(ctx, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel unknown: %v", err)
	}
}
