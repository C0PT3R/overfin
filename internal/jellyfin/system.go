package jellyfin

import (
	"context"
	"net/http"
)

// Version is the version Jellyfin says it runs, from its public
// information: no key needed.
func (c *Client) Version(ctx context.Context) (string, error) {
	var info struct{ Version string }
	if err := c.do(ctx, "public information", http.MethodGet, "/System/Info/Public", Auth{DeviceID: "overfin-check"}, nil, &info); err != nil {
		return "", err
	}
	return info.Version, nil
}

// CountLibraries counts the server's libraries. Only an administrator may list
// them, so it also tells whether admin is an administrator's key. Nothing
// about the libraries is kept but their number.
func (c *Client) CountLibraries(ctx context.Context, admin Auth) (int, error) {
	var res struct{ Items []struct{} }
	if err := c.do(ctx, "libraries", http.MethodGet, "/Library/MediaFolders", admin, nil, &res); err != nil {
		return 0, err
	}
	return len(res.Items), nil
}
