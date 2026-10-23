package jellyfin

import (
	"context"
	"net/http"
	"net/url"
)

// Plugin is a plugin installed on the server.
type Plugin struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Version string `json:"Version"`
	// Status is Jellyfin's: Active, Restart (installed, waiting for a
	// restart), Disabled, Malfunctioned…
	Status string `json:"Status"`
}

// Plugins lists the installed plugins.
func (c *Client) Plugins(ctx context.Context, admin Auth) ([]Plugin, error) {
	var out []Plugin
	err := c.do(ctx, "plugins", http.MethodGet, "/Plugins", admin, nil, &out)
	return out, err
}

// Repository is where Jellyfin finds plugins.
type Repository struct {
	Name    string `json:"Name"`
	URL     string `json:"Url"`
	Enabled bool   `json:"Enabled"`
}

// Repositories lists the server's plugin repositories.
func (c *Client) Repositories(ctx context.Context, admin Auth) ([]Repository, error) {
	var out []Repository
	err := c.do(ctx, "plugin repositories", http.MethodGet, "/Repositories", admin, nil, &out)
	return out, err
}

// SetRepositories replaces the server's plugin repositories.
func (c *Client) SetRepositories(ctx context.Context, admin Auth, repos []Repository) error {
	return c.do(ctx, "setting plugin repositories", http.MethodPost, "/Repositories", admin, repos, nil)
}

// PackageVersion is one version of a plugin, as its repository lists it.
type PackageVersion struct {
	Version   string `json:"version"`
	Checksum  string `json:"checksum"`
	SourceURL string `json:"sourceUrl"`
}

// PackageVersions lists a plugin's versions in the server's repositories.
func (c *Client) PackageVersions(ctx context.Context, admin Auth, name, guid string) ([]PackageVersion, error) {
	var out struct {
		Versions []PackageVersion `json:"versions"`
	}
	err := c.doQuery(ctx, "plugin package", http.MethodGet, "/Packages/"+name, url.Values{"assemblyGuid": {guid}}, admin, nil, &out)
	return out.Versions, err
}

// InstallPackage installs a plugin's version from a repository; it's
// active once the server restarts.
func (c *Client) InstallPackage(ctx context.Context, admin Auth, name, guid, version, repository string) error {
	q := url.Values{"assemblyGuid": {guid}, "version": {version}, "repositoryUrl": {repository}}
	return c.doQuery(ctx, "installing a plugin", http.MethodPost, "/Packages/Installed/"+name, q, admin, nil, nil)
}

// PendingRestart says whether the server waits for a restart.
func (c *Client) PendingRestart(ctx context.Context, admin Auth) (bool, error) {
	var info struct{ HasPendingRestart bool }
	err := c.do(ctx, "system information", http.MethodGet, "/System/Info", admin, nil, &info)
	return info.HasPendingRestart, err
}

// Restart restarts the server; it's unavailable for a few seconds.
func (c *Client) Restart(ctx context.Context, admin Auth) error {
	return c.do(ctx, "restarting", http.MethodPost, "/System/Restart", admin, nil, nil)
}
