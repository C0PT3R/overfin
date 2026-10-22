package api_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// A batch answers each request as it would be alone, in order; it holds
// only the API's JSON GET requests, and at most 20.
func TestBatch(t *testing.T) {
	srv, _ := newServer(t)
	code, body := post(t, srv, "/api/v1/batch", `{"requests":["/api/v1/me","/api/v1/libraries","/api/v1/titles/title_movie_tmdb_555"]}`)
	var res struct {
		Responses []struct {
			Status int
			Body   json.RawMessage
		}
	}
	json.Unmarshal([]byte(body), &res)
	if code != 200 || len(res.Responses) != 3 || res.Responses[0].Status != 200 || res.Responses[1].Status != 200 ||
		!strings.Contains(string(res.Responses[1].Body), `"items"`) || res.Responses[2].Status != 404 ||
		!strings.Contains(string(res.Responses[2].Body), "not-found") {
		t.Fatalf("batch: %d %s", code, body)
	}
	for _, bad := range []string{
		`{"requests":[]}`,
		`{"requests":["/api/v1/images/x"]}`,
		`{"requests":["/api/v1/batch"]}`,
		`{"requests":["/api/v1/../admin"]}`,
		`{"requests":["https://evil.example/api/v1/me"]}`,
		`{"requests":["/elsewhere"]}`,
		`{"requests":["/api/v1/me"` + strings.Repeat(`,"/api/v1/me"`, 20) + `]}`,
	} {
		if code, _ := post(t, srv, "/api/v1/batch", bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}
