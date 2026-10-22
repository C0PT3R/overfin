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
	"time"

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
			if u.ID == "" || u.ID == staticAPIUser {
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

// staticAPIUser is the user Pocket ID's STATIC_API_KEY acts as: not a
// person.
const staticAPIUser = "00000000-0000-0000-0000-000000000000"

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

// Client is the OpenID Connect client an app has in Pocket ID.
type Client struct {
	ID, Name string
	// Secret is the client secret the app signs in with (16 characters or
	// more): Pocket ID is given it, never asked for it.
	Secret string
	// CallbackURLs are where Pocket ID may send people back to.
	CallbackURLs []string
}

// Setup makes sure Pocket ID has the client (made, or its name and
// callback URLs brought up to date; the secret added when it's missing)
// and the groups, by name. It can run at every start.
func (p *PocketID) Setup(ctx context.Context, c Client, groups []string) error {
	body := map[string]any{"id": c.ID, "name": c.Name, "callbackURLs": c.CallbackURLs, "isPublic": false,
		"pkceEnabled": true, "skipConsent": true}
	var existing struct{ ID string }
	err := p.do(ctx, http.MethodGet, "/api/oidc/clients/"+url.PathEscape(c.ID), nil, &existing)
	switch {
	case errors.Is(err, ErrNotFound):
		var made struct {
			CreatedSecret *struct{ ID string } `json:"createdSecret"`
		}
		if err := p.do(ctx, http.MethodPost, "/api/oidc/clients", body, &made); err != nil {
			return fmt.Errorf("making the OIDC client: %w", err)
		}
		// The secret Pocket ID made goes: the app has its own.
		if made.CreatedSecret != nil && made.CreatedSecret.ID != "" {
			if err := p.do(ctx, http.MethodDelete, "/api/oidc/clients/"+url.PathEscape(c.ID)+"/secrets/"+url.PathEscape(made.CreatedSecret.ID), nil, nil); err != nil {
				return fmt.Errorf("removing the generated secret: %w", err)
			}
		}
	case err != nil:
		return fmt.Errorf("reading the OIDC client: %w", err)
	default:
		delete(body, "id")
		if err := p.do(ctx, http.MethodPut, "/api/oidc/clients/"+url.PathEscape(c.ID), body, nil); err != nil {
			return fmt.Errorf("updating the OIDC client: %w", err)
		}
	}
	if err := p.ensureSecret(ctx, c); err != nil {
		return err
	}
	for _, g := range groups {
		if err := p.ensureGroup(ctx, g); err != nil {
			return err
		}
	}
	return nil
}

// ensureSecret adds the client's secret unless Pocket ID has it already:
// it only shows each secret's first characters, which must match.
func (p *PocketID) ensureSecret(ctx context.Context, c Client) error {
	path := "/api/oidc/clients/" + url.PathEscape(c.ID) + "/secrets"
	var raw json.RawMessage
	if err := p.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return fmt.Errorf("listing the client's secrets: %w", err)
	}
	type secret struct {
		Prefix   string `json:"prefix"`
		IsActive bool   `json:"isActive"`
	}
	var list []secret
	if json.Unmarshal(raw, &list) != nil {
		var page struct{ Data []secret }
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("listing the client's secrets: %w", err)
		}
		list = page.Data
	}
	for _, s := range list {
		if s.IsActive && s.Prefix != "" && strings.HasPrefix(c.Secret, s.Prefix) {
			return nil
		}
	}
	if err := p.do(ctx, http.MethodPost, path, map[string]any{"secret": c.Secret}, nil); err != nil {
		return fmt.Errorf("adding the client's secret: %w", err)
	}
	return nil
}

// ensureGroup makes a group unless Pocket ID has one of that name.
func (p *PocketID) ensureGroup(ctx context.Context, name string) error {
	id, err := p.groupID(ctx, name)
	if err != nil || id != "" {
		return err
	}
	if err := p.do(ctx, http.MethodPost, "/api/user-groups", map[string]string{"name": name, "friendlyName": name}, nil); err != nil {
		return fmt.Errorf("making the group %q: %w", name, err)
	}
	return nil
}

// groupID is the ID of the group of that name; empty when there's none.
func (p *PocketID) groupID(ctx context.Context, name string) (string, error) {
	var res struct {
		Data []struct{ ID, Name string } `json:"data"`
	}
	q := url.Values{"search": {name}, "pagination[limit]": {"100"}}
	if err := p.do(ctx, http.MethodGet, "/api/user-groups?"+q.Encode(), nil, &res); err != nil {
		return "", fmt.Errorf("listing groups: %w", err)
	}
	for _, g := range res.Data {
		if g.Name == name {
			return g.ID, nil
		}
	}
	return "", nil
}

// SignupToken makes a sign-up token for one person, valid for ttl, whose
// account joins the groups (by name; they must exist). People use it at
// Pocket ID's /st/<token>.
func (p *PocketID) SignupToken(ctx context.Context, ttl time.Duration, groups []string) (string, error) {
	ids := []string{}
	for _, name := range groups {
		id, err := p.groupID(ctx, name)
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("connector: Pocket ID has no group %q", name)
		}
		ids = append(ids, id)
	}
	var res struct {
		Token string `json:"token"`
	}
	body := map[string]any{"ttl": int(ttl.Seconds()), "usageLimit": 1, "userGroupIds": ids}
	if err := p.do(ctx, http.MethodPost, "/api/signup-tokens", body, &res); err != nil {
		return "", fmt.Errorf("making a sign-up token: %w", err)
	}
	if res.Token == "" {
		return "", errors.New("connector: Pocket ID gave no sign-up token")
	}
	return res.Token, nil
}

// Signups is Pocket ID's sign-up setting: "disabled", "withToken" or
// "open". Its sign-up page refuses tokens when it's "disabled".
func (p *PocketID) Signups(ctx context.Context) (string, error) {
	var res []struct{ Key, Value string }
	if err := p.do(ctx, http.MethodGet, "/api/application-configuration", nil, &res); err != nil {
		return "", err
	}
	for _, c := range res {
		if c.Key == "allowUserSignups" {
			return c.Value, nil
		}
	}
	return "", nil
}

// FirstAdmin makes the first person in an empty Pocket ID: an admin there,
// by email (also their username), in the groups (by name; they must
// exist). It reports whether it made them: when Pocket ID already has
// someone, it changes nothing.
func (p *PocketID) FirstAdmin(ctx context.Context, email string, groups []string) (bool, error) {
	var res struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := p.do(ctx, http.MethodGet, "/api/users?pagination[limit]=2", nil, &res); err != nil {
		return false, err
	}
	for _, u := range res.Data {
		if u.ID != staticAPIUser {
			return false, nil
		}
	}
	ids := []string{}
	for _, name := range groups {
		id, err := p.groupID(ctx, name)
		if err != nil {
			return false, err
		}
		if id == "" {
			return false, fmt.Errorf("connector: Pocket ID has no group %q", name)
		}
		ids = append(ids, id)
	}
	body := map[string]any{"username": email, "email": email, "emailVerified": true, "isAdmin": true, "userGroupIds": ids}
	if err := p.do(ctx, http.MethodPost, "/api/users", body, nil); err != nil {
		return false, fmt.Errorf("making the first admin: %w", err)
	}
	return true, nil
}
