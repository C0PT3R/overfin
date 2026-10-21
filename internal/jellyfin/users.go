package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// CreateUser creates a Jellyfin user and returns its ID. Admin only.
func (c *Client) CreateUser(ctx context.Context, admin Auth, name, password string) (string, error) {
	var out struct{ Id string }
	in := map[string]string{"Name": name, "Password": password}
	if err := c.do(ctx, "create user", http.MethodPost, "/Users/New", admin, in, &out); err != nil {
		return "", err
	}
	return NormalizeID(out.Id)
}

// DeleteUser deletes a Jellyfin user. Admin only.
func (c *Client) DeleteUser(ctx context.Context, admin Auth, userID string) error {
	id, err := NormalizeID(userID)
	if err != nil {
		return err
	}
	return c.do(ctx, "delete user", http.MethodDelete, "/Users/"+id, admin, nil, nil)
}

// Policy is a user's full Jellyfin policy, kept as raw fields: Jellyfin
// requires the whole object back, including fields Overfin doesn't know.
type Policy map[string]any

// UserPolicy reads a user's current policy. Admin only.
func (c *Client) UserPolicy(ctx context.Context, admin Auth, userID string) (Policy, error) {
	id, err := NormalizeID(userID)
	if err != nil {
		return nil, err
	}
	var out struct{ Policy Policy }
	if err := c.do(ctx, "get user", http.MethodGet, "/Users/"+id, admin, nil, &out); err != nil {
		return nil, err
	}
	if out.Policy == nil {
		return nil, errors.New("jellyfin: user has no policy")
	}
	return out.Policy, nil
}

// SetUserPolicy replaces a user's policy. Admin only.
func (c *Client) SetUserPolicy(ctx context.Context, admin Auth, userID string, p Policy) error {
	id, err := NormalizeID(userID)
	if err != nil {
		return err
	}
	return c.do(ctx, "set policy", http.MethodPost, "/Users/"+id+"/Policy", admin, p, nil)
}

// LockDown applies the restrictions every Overfin-managed user gets: no
// administration, hidden from login screens, no deleting, sharing or
// managing anything, no controlling other users. Playback stays allowed.
func (p Policy) LockDown() {
	for k, v := range map[string]any{
		"IsAdministrator":                  false,
		"IsHidden":                         true,
		"IsDisabled":                       false,
		"EnableContentDeletion":            false,
		"EnableContentDeletionFromFolders": []string{},
		"EnableCollectionManagement":       false,
		"EnableSubtitleManagement":         false,
		"EnableLyricManagement":            false,
		"EnableLiveTvManagement":           false,
		"EnablePublicSharing":              false,
		"EnableRemoteControlOfOtherUsers":  false,
		"EnableSharedDeviceControl":        false,
		"EnableContentDownloading":         false,
		"EnableUserPreferenceAccess":       false,
		"SyncPlayAccess":                   "None",
		"EnableMediaPlayback":              true,
		"EnableAudioPlaybackTranscoding":   true,
		"EnableVideoPlaybackTranscoding":   true,
		"EnablePlaybackRemuxing":           true,
		"EnableRemoteAccess":               true,
	} {
		p[k] = v
	}
}

// UserName returns a user's name. Admin only.
func (c *Client) UserName(ctx context.Context, admin Auth, userID string) (string, error) {
	id, err := NormalizeID(userID)
	if err != nil {
		return "", err
	}
	var out struct{ Name string }
	if err := c.do(ctx, "get user", http.MethodGet, "/Users/"+id, admin, nil, &out); err != nil {
		return "", err
	}
	return out.Name, nil
}

// RenameUser changes a user's name. Admin only. Jellyfin takes the whole
// user back, so the current one is read first.
func (c *Client) RenameUser(ctx context.Context, admin Auth, userID, name string) error {
	id, err := NormalizeID(userID)
	if err != nil {
		return err
	}
	var user map[string]any
	if err := c.do(ctx, "get user", http.MethodGet, "/Users/"+id, admin, nil, &user); err != nil {
		return err
	}
	user["Name"] = name
	return c.doQuery(ctx, "rename user", http.MethodPost, "/Users", url.Values{"userId": {id}}, admin, user, nil)
}

// Logout ends the session a token belongs to, which revokes the token.
func (c *Client) Logout(ctx context.Context, a Auth) error {
	return c.do(ctx, "logout", http.MethodPost, "/Sessions/Logout", a, nil, nil)
}

// SetPassword gives a user a new password. Admin only.
func (c *Client) SetPassword(ctx context.Context, admin Auth, userID, password string) error {
	id, err := NormalizeID(userID)
	if err != nil {
		return err
	}
	return c.doQuery(ctx, "set password", http.MethodPost, "/Users/Password", url.Values{"userId": {id}}, admin,
		map[string]string{"NewPw": password}, nil)
}

// Authenticate logs in as a user and returns its token and ID.
func (c *Client) Authenticate(ctx context.Context, deviceID, username, password string) (token, userID string, err error) {
	var out struct {
		AccessToken string
		User        struct{ Id string }
	}
	in := map[string]string{"Username": username, "Pw": password}
	if err := c.do(ctx, "authenticate", http.MethodPost, "/Users/AuthenticateByName",
		Auth{DeviceID: deviceID}, in, &out); err != nil {
		return "", "", err
	}
	if out.AccessToken == "" {
		return "", "", errors.New("jellyfin: authenticate: no token in response")
	}
	id, err := NormalizeID(out.User.Id)
	if err != nil {
		return "", "", err
	}
	return out.AccessToken, id, nil
}

// Limited says whether a policy has a rating limit.
func (p Policy) Limited() bool {
	blocked, _ := p["BlockUnratedItems"].([]any)
	return p["MaxParentalRating"] != nil || p["MaxParentalSubRating"] != nil || len(blocked) > 0
}

// Unlimited removes any rating ceiling.
func (p Policy) Unlimited() {
	p["MaxParentalRating"] = nil
	p["MaxParentalSubRating"] = nil
	p["BlockUnratedItems"] = []string{}
}
