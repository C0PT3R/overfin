package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/upstream"
)

// PocketID reads people from Pocket ID's API (/api), with an API key (its
// STATIC_API_KEY, or an admin's key) in the X-API-Key header.
type PocketID struct {
	// BaseURL is Pocket ID's address, e.g. "https://auth.example.org".
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// ErrPocketIDKey: Pocket ID refused the key.
var ErrPocketIDKey = errors.New("connector: Pocket ID refused the API key: it must be its STATIC_API_KEY, or an admin's API key")

// People returns everyone Pocket ID knows, by their user ID (the subject
// of its logins).
func (p *PocketID) People(ctx context.Context) (map[string]Person, error) {
	out := map[string]Person{}
	for page := 1; len(out) < maxPeople; page++ {
		var res struct {
			Data []struct {
				ID          string  `json:"id"`
				Username    string  `json:"username"`
				Email       *string `json:"email"`
				FirstName   string  `json:"firstName"`
				LastName    *string `json:"lastName"`
				DisplayName string  `json:"displayName"`
				Disabled    bool    `json:"disabled"`
			} `json:"data"`
			Pagination struct {
				TotalPages int `json:"totalPages"`
			} `json:"pagination"`
		}
		q := url.Values{"pagination[page]": {strconv.Itoa(page)}, "pagination[limit]": {"100"}}
		if err := p.do(ctx, http.MethodGet, "/api/users?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, u := range res.Data {
			if u.ID == "" {
				continue
			}
			name := u.DisplayName
			if name == "" {
				name = strings.TrimSpace(u.FirstName + " " + deref(u.LastName))
			}
			out[u.ID] = Person{Username: u.Username, Name: name, Email: deref(u.Email), Active: !u.Disabled}
		}
		if page >= res.Pagination.TotalPages || len(res.Data) == 0 {
			break
		}
	}
	return out, nil
}

// Check reads one user, to tell whether the key works.
func (p *PocketID) Check(ctx context.Context) error {
	var res struct{}
	return p.do(ctx, http.MethodGet, "/api/users?pagination[limit]=1", nil, &res)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// do calls Pocket ID's API: body as JSON when not nil, the answer decoded
// into out when not nil.
func (p *PocketID) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(p.BaseURL, "/")+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", p.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := p.HTTP.Do(req)
	if err != nil {
		return upstream.Plain(err)
	}
	defer res.Body.Close()
	limited := io.LimitReader(res.Body, 8<<20)
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		io.Copy(io.Discard, limited)
		return ErrPocketIDKey
	case res.StatusCode == http.StatusNotFound:
		io.Copy(io.Discard, limited)
		return ErrNotFound
	case res.StatusCode < 200 || res.StatusCode > 299:
		io.Copy(io.Discard, limited)
		return fmt.Errorf("connector: Pocket ID answered %d to %s %s", res.StatusCode, method, strings.SplitN(path, "?", 2)[0])
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, limited)
		return nil
	}
	return json.NewDecoder(limited).Decode(out)
}

// ErrNotFound: Pocket ID has no such thing.
var ErrNotFound = errors.New("connector: not found")
