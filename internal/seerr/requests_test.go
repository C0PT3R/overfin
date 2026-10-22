package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
			io.WriteString(w, `{"id":42,"username":"overfin-abc"}`)
		case "GET /api/v1/user/42/settings/main":
			io.WriteString(w, `{"username":"overfin-abc","email":"abc@overfin.invalid","locale":"fr","movieQuotaLimit":3}`)
		case "POST /api/v1/user/42/settings/main":
			w.WriteHeader(http.StatusOK)
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
			if r.URL.Query().Get("requestedBy") == "" {
				// Everyone's: two pages, the second one short.
				if r.URL.Query().Get("skip") == "0" {
					var page []string
					for i := 0; i < 100; i++ {
						page = append(page, fmt.Sprintf(`{"id":%d,"status":3,"type":"movie","media":{"tmdbId":%d,"status":1}}`, 100+i, 1000+i))
					}
					page[0] = `{"id":1,"status":1,"type":"movie","media":{"tmdbId":949,"status":2}}`
					page[1] = `{"id":2,"status":2,"type":"tv","media":{"tmdbId":1399,"status":4},"seasons":[{"seasonNumber":3}]}`
					io.WriteString(w, `{"results":[`+strings.Join(page, ",")+`]}`)
				} else {
					io.WriteString(w, `{"results":[{"id":3,"status":2,"type":"movie","media":{"tmdbId":550,"status":5}}]}`)
				}
				return
			}
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
	// Everyone's open requests: pending or approved, not available yet.
	open, err := c.OpenRequests(ctx)
	if err != nil || len(open) != 2 || open[0].TMDBID != 949 || open[1].Kind != "series" || !slices.Equal(open[1].Seasons, []int{3}) {
		t.Errorf("open requests: %+v %v", open, err)
	}
	if name, ok, err := c.Username(ctx, 42); !ok || err != nil || name != "overfin-abc" {
		t.Errorf("user 42: %q %v %v", name, ok, err)
	}
	if _, ok, err := c.Username(ctx, 43); ok || err != nil {
		t.Errorf("user 43: %v %v", ok, err)
	}
	// Renamed: the settings sent back whole, only the name changed.
	if err := c.Rename(ctx, 42, "Alice Tremblay"); err != nil {
		t.Fatal(err)
	}
	if last := bodies[len(bodies)-1]; last["username"] != "Alice Tremblay" || last["email"] != "abc@overfin.invalid" ||
		last["locale"] != "fr" || last["movieQuotaLimit"] != float64(3) {
		t.Errorf("rename: %v", last)
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
