// Package jellyfin is Overfin's adapter for the Jellyfin 12 API.
//
// Every call Overfin makes to Jellyfin is a typed method here (SECURITY.md
// rule 1). The client never follows redirects, bounds response sizes and
// times out (rule 6), validates IDs it receives (rule 7), and never passes
// Jellyfin's error text on to anyone.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// maxResponse bounds every JSON response read from Jellyfin.
const maxResponse = 4 << 20 // 4 MiB

// Client talks to one Jellyfin server. Its address may change while it's
// in use (SetURL), for the same server reached another way.
type Client struct {
	base    atomic.Pointer[url.URL]
	http    *http.Client
	version string
}

// New returns a client for the Jellyfin server at baseURL. version is
// Overfin's own version, reported to Jellyfin as the client version.
func New(baseURL, version string) (*Client, error) {
	c := &Client{
		version: version,
		http: &http.Client{
			Timeout: 15 * time.Second,
			// Never follow a redirect from upstream (SECURITY.md rule 6).
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	if err := c.SetURL(baseURL); err != nil {
		return nil, err
	}
	return c, nil
}

// SetURL points the client at a new address. Requests already sent keep
// the old one.
func (c *Client) SetURL(baseURL string) error {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return err
	}
	c.base.Store(u)
	return nil
}

// baseURL is a copy of the current address, for one request to build on.
func (c *Client) baseURL() url.URL { return *c.base.Load() }

// StatusError is a non-success answer from Jellyfin. Only the status is
// kept; Jellyfin's message never reaches clients.
type StatusError struct {
	Op     string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("jellyfin: %s: HTTP %d", e.Op, e.Status)
}

// IsStatus reports whether err is a StatusError with the given status.
func IsStatus(err error, status int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}

// Auth is how a request identifies itself to Jellyfin. DeviceID should be
// stable per profile, so Jellyfin's device list doesn't fill with ghosts.
type Auth struct {
	Token    string
	DeviceID string
}

// header builds Jellyfin's Authorization header. Values are quoted, so they
// must not contain quotes; the IDs and tokens Overfin uses never do.
func (c *Client) header(a Auth) string {
	h := fmt.Sprintf(`MediaBrowser Client="Overfin", Device="Overfin gateway", DeviceId=%q, Version=%q`,
		a.DeviceID, c.version)
	if a.Token != "" {
		h += fmt.Sprintf(`, Token=%q`, a.Token)
	}
	return h
}

// do sends one request. path is fixed by the caller, with IDs already
// validated; in is encoded as JSON when not nil; out is decoded when not nil.
func (c *Client) do(ctx context.Context, op, method, path string, a Auth, in, out any) error {
	return c.doQuery(ctx, op, method, path, nil, a, in, out)
}

// doQuery is do with query parameters, which are encoded into the URL's
// query, never pasted into its path.
func (c *Client) doQuery(ctx context.Context, op, method, path string, query url.Values, a Auth, in, out any) error {
	u := c.baseURL()
	u.Path += path
	u.RawQuery = query.Encode()

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.header(a))
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin: %s: %w", op, err)
	}
	defer res.Body.Close()
	limited := io.LimitReader(res.Body, maxResponse)

	if res.StatusCode < 200 || res.StatusCode > 299 {
		io.Copy(io.Discard, limited)
		return &StatusError{Op: op, Status: res.StatusCode}
	}
	if out == nil {
		io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("jellyfin: %s: decoding response: %w", op, err)
	}
	return nil
}

// ---- IDs --------------------------------------------------------------------

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

// NormalizeID validates a Jellyfin ID (a GUID, with or without dashes) and
// returns it as 32 lowercase hex characters. Upstream IDs are checked like
// client input (SECURITY.md rule 7).
func NormalizeID(id string) (string, error) {
	if !guidPattern.MatchString(id) {
		return "", errors.New("jellyfin: malformed ID")
	}
	return strings.ToLower(strings.ReplaceAll(id, "-", "")), nil
}
