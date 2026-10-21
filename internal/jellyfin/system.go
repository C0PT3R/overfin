package jellyfin

import (
	"context"
	"net/http"
)

// PublicInfo is the version Jellyfin says it runs and its server ID, which
// stays the same whatever address it's reached at. No key needed.
func (c *Client) PublicInfo(ctx context.Context) (version, serverID string, err error) {
	var info struct{ Version, Id string }
	if err := c.do(ctx, "public information", http.MethodGet, "/System/Info/Public", Auth{DeviceID: "overfin-check"}, nil, &info); err != nil {
		return "", "", err
	}
	return info.Version, info.Id, nil
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
