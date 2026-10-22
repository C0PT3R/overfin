// Package connector talks to an identity provider's own administration API,
// for what OpenID Connect doesn't cover: who the people behind accounts
// are, and (later) inviting new ones. Connectors are optional and
// provider-specific; Overfin works without them.
//
// What a connector reads is shown live in the control panel and never
// stored (SECURITY.md rule 40).
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

// Person is what the identity provider knows about someone.
type Person struct {
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	// Active is false when the provider disabled them.
	Active bool `json:"active"`
}

// Authentik reads people from Authentik's API (/api/v3). Its token needs
// only permission to view users.
type Authentik struct {
	// BaseURL is Authentik's address, e.g. "https://auth.example.org".
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// maxPeople caps how many users one listing reads.
const maxPeople = 20_000

// People returns everyone the provider knows, by the user ID it gives in
// logins (OpenID Connect's subject, Authentik's hashed "uid").
func (a *Authentik) People(ctx context.Context) (map[string]Person, error) {
	out := map[string]Person{}
	for page := 1; len(out) < maxPeople; page++ {
		u := strings.TrimSuffix(a.BaseURL, "/") + "/api/v3/core/users/?" +
			url.Values{"page": {strconv.Itoa(page)}, "page_size": {"100"}}.Encode()
		var res struct {
			Pagination struct {
				Next int `json:"next"`
			} `json:"pagination"`
			Results []struct {
				UID      string `json:"uid"`
				Username string `json:"username"`
				Name     string `json:"name"`
				Email    string `json:"email"`
				IsActive bool   `json:"is_active"`
			} `json:"results"`
		}
		if err := a.get(ctx, u, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Results {
			if r.UID != "" {
				out[r.UID] = Person{Username: r.Username, Name: r.Name, Email: r.Email, Active: r.IsActive}
			}
		}
		if res.Pagination.Next == 0 || len(res.Results) == 0 {
			break
		}
	}
	return out, nil
}

// ErrUnauthorized: the token is wrong, expired, or lacks permission.
var ErrUnauthorized = errors.New("connector: Authentik refused the token: it must be an API Token " +
	"(intent \"API\", not an app password), not expired, for an account allowed to view users")

// Check reads one user, to tell whether the token works.
func (a *Authentik) Check(ctx context.Context) error {
	var res struct{}
	return a.get(ctx, strings.TrimSuffix(a.BaseURL, "/")+"/api/v3/core/users/?page_size=1", &res)
}

func (a *Authentik) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Accept", "application/json")
	res, err := a.HTTP.Do(req)
	if err != nil {
		return upstream.Plain(err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("connector: Authentik answered %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
