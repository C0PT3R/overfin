package server

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/overfin/server/internal/problem"
)

// Batch (POST /api/v1/batch) answers several of the API's GET requests in
// one: an app's home page, say, with each row its own request. Each runs
// as it would alone, through the same routes, checks and limits, for the
// same caller; the app chooses which, so no one's layout is imposed.

// maxBatch is how many requests one batch may hold.
const maxBatch = 20

// batchParallel is how many of a batch's requests run at the same time.
const batchParallel = 6

// notBatched are the GET routes a batch refuses: they answer with media or
// redirects, not JSON.
var notBatched = []string{"/api/v1/images/", "/api/v1/stream/", "/api/v1/auth/"}

type batchAnswer struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// batchable says whether a batch may hold a request for target (a path
// with its query), and returns it parsed.
func batchable(target string) (*url.URL, bool) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/api/v1/") || path.Clean(u.Path) != u.Path || u.Path == "/api/v1/batch" {
		return nil, false
	}
	for _, p := range notBatched {
		if strings.HasPrefix(u.Path, p) {
			return nil, false
		}
	}
	return u, true
}

// batch runs each request through api, as the caller, and answers with
// each one's status and JSON body, in the order asked. Cookies the
// requests set are passed on.
func batch(api http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []string `json:"requests"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || len(body.Requests) == 0 || len(body.Requests) > maxBatch {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
		targets := make([]*url.URL, len(body.Requests))
		for i, t := range body.Requests {
			u, ok := batchable(t)
			if !ok {
				problem.Send(w, problem.InvalidRequest, "not a request a batch can hold: "+strings.ReplaceAll(t, "\n", ""))
				return
			}
			targets[i] = u
		}
		answers := make([]batchAnswer, len(targets))
		cookies := make([][]string, len(targets))
		slots := make(chan struct{}, batchParallel)
		var wg sync.WaitGroup
		for i, u := range targets {
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer func() { <-slots; wg.Done() }()
				sub := r.Clone(r.Context())
				sub.Method, sub.URL, sub.RequestURI = http.MethodGet, u, u.RequestURI()
				sub.Body, sub.ContentLength = http.NoBody, 0
				sub.Header.Del("Content-Type")
				rec := httptest.NewRecorder()
				api.ServeHTTP(rec, sub)
				answers[i].Status = rec.Code
				cookies[i] = rec.Header().Values("Set-Cookie")
				if mt, _, _ := mime.ParseMediaType(rec.Header().Get("Content-Type")); (mt == "application/json" ||
					mt == "application/problem+json") && json.Valid(rec.Body.Bytes()) {
					answers[i].Body = rec.Body.Bytes()
				}
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for _, cs := range cookies {
			for _, c := range cs {
				if !seen[c] {
					seen[c] = true
					w.Header().Add("Set-Cookie", c)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"responses": answers})
	}
}
