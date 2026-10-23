package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Startup is the language and country a fresh Jellyfin's first-run setup
// gets.
type Startup struct {
	UICulture string // e.g. "en-US"
	Country   string // e.g. "US"
	Language  string // e.g. "en"
}

// RunStartup runs a fresh Jellyfin's first-run setup: its language and
// country, its administrator (name and password), remote access on (Overfin
// may run on another machine), port mapping off. Only a fresh server
// accepts it.
func (c *Client) RunStartup(ctx context.Context, s Startup, name, password string) error {
	a := Auth{DeviceID: "overfin-setup"}
	if err := c.do(ctx, "startup configuration", http.MethodPost, "/Startup/Configuration", a,
		map[string]string{"UICulture": s.UICulture, "MetadataCountryCode": s.Country, "PreferredMetadataLanguage": s.Language}, nil); err != nil {
		return err
	}
	// Jellyfin makes its first user when it's first asked for.
	if err := c.do(ctx, "startup user", http.MethodGet, "/Startup/User", a, nil, &struct{}{}); err != nil {
		return err
	}
	if err := c.do(ctx, "startup user", http.MethodPost, "/Startup/User", a,
		map[string]string{"Name": name, "Password": password}, nil); err != nil {
		return err
	}
	if err := c.do(ctx, "startup remote access", http.MethodPost, "/Startup/RemoteAccess", a,
		map[string]bool{"EnableRemoteAccess": true, "EnableAutomaticPortMapping": false}, nil); err != nil {
		return err
	}
	return c.do(ctx, "startup complete", http.MethodPost, "/Startup/Complete", a, nil, nil)
}

// CreateKey makes an API key for app and returns it. Admin only. Jellyfin
// doesn't return the key it made: it's the one in its list that wasn't
// there before. The other keys are only compared, never kept.
func (c *Client) CreateKey(ctx context.Context, admin Auth, app string) (string, error) {
	before, err := c.keys(ctx, admin)
	if err != nil {
		return "", err
	}
	if err := c.doQuery(ctx, "create key", http.MethodPost, "/Auth/Keys", url.Values{"app": {app}}, admin, nil, nil); err != nil {
		return "", err
	}
	after, err := c.keys(ctx, admin)
	if err != nil {
		return "", err
	}
	made := ""
	for k, name := range after {
		if _, ok := before[k]; !ok && name == app {
			if made != "" {
				return "", errors.New("jellyfin: create key: two new keys appeared at once")
			}
			made = k
		}
	}
	if made == "" {
		return "", errors.New("jellyfin: create key: the new key isn't in Jellyfin's list")
	}
	return made, nil
}

// keys is the server's API keys, each with its app's name.
func (c *Client) keys(ctx context.Context, admin Auth) (map[string]string, error) {
	var res struct {
		Items []struct{ AccessToken, AppName string }
	}
	if err := c.do(ctx, "keys", http.MethodGet, "/Auth/Keys", admin, nil, &res); err != nil {
		return nil, err
	}
	keys := make(map[string]string, len(res.Items))
	for _, k := range res.Items {
		keys[k.AccessToken] = k.AppName
	}
	return keys, nil
}

// Me is the name of the user a token belongs to, and whether they're an
// administrator.
func (c *Client) Me(ctx context.Context, a Auth) (name string, admin bool, err error) {
	var me struct {
		Name   string
		Policy struct{ IsAdministrator bool }
	}
	if err := c.do(ctx, "me", http.MethodGet, "/Users/Me", a, nil, &me); err != nil {
		return "", false, err
	}
	return me.Name, me.Policy.IsAdministrator, nil
}

// UserNames is the names of the server's users. Admin only.
func (c *Client) UserNames(ctx context.Context, admin Auth) ([]string, error) {
	var users []struct{ Name string }
	if err := c.do(ctx, "users", http.MethodGet, "/Users", admin, nil, &users); err != nil {
		return nil, err
	}
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	return names, nil
}
