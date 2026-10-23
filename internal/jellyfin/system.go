package jellyfin

import (
	"context"
	"net/http"
)

// Server is what Jellyfin tells anyone about itself, no key needed.
type Server struct {
	Version string
	// ID stays the same whatever address the server is reached at.
	ID   string
	Name string
	// SetUp is false while Jellyfin's first-run setup hasn't run.
	SetUp bool
}

// Public is what the server says about itself.
func (c *Client) Public(ctx context.Context) (Server, error) {
	var info struct {
		Version, Id, ServerName string
		StartupWizardCompleted  bool
	}
	if err := c.do(ctx, "public information", http.MethodGet, "/System/Info/Public", Auth{DeviceID: "overfin-check"}, nil, &info); err != nil {
		return Server{}, err
	}
	return Server{Version: info.Version, ID: info.Id, Name: info.ServerName, SetUp: info.StartupWizardCompleted}, nil
}

// CountLibraries counts the server's libraries. Only an administrator may list
// them, so it also tells whether admin is an administrator's key. Nothing
// about the libraries is kept but their number. Jellyfin's own folders for
// playlists and collections aren't libraries.
func (c *Client) CountLibraries(ctx context.Context, admin Auth) (int, error) {
	var res struct{ Items []struct{ CollectionType string } }
	if err := c.do(ctx, "libraries", http.MethodGet, "/Library/MediaFolders", admin, nil, &res); err != nil {
		return 0, err
	}
	n := 0
	for _, f := range res.Items {
		if f.CollectionType != "playlists" && f.CollectionType != "boxsets" {
			n++
		}
	}
	return n, nil
}
