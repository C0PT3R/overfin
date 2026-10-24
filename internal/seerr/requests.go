package seerr

import (
	"bytes"
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

// Requests, made in Seerr on behalf of Overfin's accounts: each has a Seerr
// user of its own, under the account's name (a placeholder address no one
// can write to, a password no one keeps), which Seerr's own settings give
// its permissions, auto-approval and quotas.

var (
	// ErrQuota: the user's request quota is used up.
	ErrQuota = errors.New("seerr: the quota is used up")
	// ErrExists: it's already requested, or available.
	ErrExists = errors.New("seerr: already requested")
	// ErrRefused: Seerr refused the request otherwise (the user may not
	// request, say).
	ErrRefused = errors.New("seerr: the request was refused")
)

// send makes a call that changes something, with body as JSON (none when
// nil), and decodes the answer into out (when not nil).
func (c *Client) send(ctx context.Context, method, path string, body, out any) error {
	return c.sendAs(ctx, 0, method, path, body, out)
}

// sendAs is send as one of Seerr's users (none when 0): with the API key
// alone, Seerr acts as its first admin, whose permissions would approve
// every request and skip every quota.
func (c *Client) sendAs(ctx context.Context, userID int, method, path string, body, out any) error {
	u := *c.base
	u.Path += "/api/v1" + path
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.key)
	if userID != 0 {
		req.Header.Set("X-Api-User", strconv.Itoa(userID))
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("seerr: %s %s: %w", method, path, upstream.Plain(err))
	}
	defer res.Body.Close()
	limited := io.LimitReader(res.Body, maxResponse)
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		io.Copy(io.Discard, limited)
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		io.Copy(io.Discard, limited)
		return ErrNotFound
	case res.StatusCode == http.StatusConflict:
		io.Copy(io.Discard, limited)
		return ErrExists
	case res.StatusCode == http.StatusForbidden:
		// Seerr says why in its message; only the quota matters here.
		var problem struct{ Message string }
		json.NewDecoder(limited).Decode(&problem)
		if strings.Contains(strings.ToLower(problem.Message), "quota") {
			return ErrQuota
		}
		return ErrRefused
	case res.StatusCode < 200 || res.StatusCode > 299:
		io.Copy(io.Discard, limited)
		return fmt.Errorf("seerr: %s %s answered %d", method, path, res.StatusCode)
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("seerr: %s %s: decoding the answer: %w", method, path, err)
	}
	return nil
}

// CreateUser makes a Seerr user and returns its ID. A password is always
// given (then thrown away by the caller): without one, Seerr would email a
// generated one.
func (c *Client) CreateUser(ctx context.Context, username, email, password string) (int, error) {
	var user struct{ ID int }
	err := c.send(ctx, http.MethodPost, "/user", map[string]string{"username": username, "email": email, "password": password}, &user)
	if err != nil {
		return 0, err
	}
	if user.ID <= 0 {
		return 0, errors.New("seerr: the new user has no ID")
	}
	return user.ID, nil
}

// Username is a user's name in Seerr; ok is false when Seerr has no such
// user.
func (c *Client) Username(ctx context.Context, id int) (name string, ok bool, err error) {
	var user struct {
		ID       int
		Username *string
	}
	err = c.get(ctx, "/user/"+strconv.Itoa(id), nil, &user)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil || user.ID != id {
		return "", false, err
	}
	if user.Username != nil {
		name = *user.Username
	}
	return name, true, nil
}

// Rename changes a user's name in Seerr. Seerr takes the user's main
// settings whole, so they're read first and sent back with only the name
// changed.
func (c *Client) Rename(ctx context.Context, id int, name string) error {
	path := "/user/" + strconv.Itoa(id) + "/settings/main"
	var settings map[string]any
	if err := c.get(ctx, path, nil, &settings); err != nil {
		return err
	}
	if settings == nil {
		return errors.New("seerr: the user has no settings")
	}
	settings["username"] = name
	return c.send(ctx, http.MethodPost, path, settings, nil)
}

// Request is a request as Seerr has it.
type Request struct {
	ID int
	// Status is the request's: 1 pending, 2 approved, 3 declined, 4
	// failed, 5 completed.
	Status int
	// MediaStatus is the title's: 2 pending, 3 processing, 4 partly
	// available, 5 available.
	MediaStatus int
	TMDBID      int
	Kind        string // movie, series
	Seasons     []int
}

type rawRequest struct {
	ID     int
	Status int
	Type   string
	Media  struct {
		TMDBID int `json:"tmdbId"`
		Status int
	}
	Seasons []struct {
		SeasonNumber int
	}
}

func (r rawRequest) request() Request {
	out := Request{ID: r.ID, Status: r.Status, MediaStatus: r.Media.Status, TMDBID: r.Media.TMDBID, Kind: "movie"}
	if r.Type == "tv" {
		out.Kind = "series"
	}
	for _, s := range r.Seasons {
		if s.SeasonNumber >= 0 && s.SeasonNumber <= 1000 {
			out.Seasons = append(out.Seasons, s.SeasonNumber)
		}
	}
	return out
}

// RequestTitle requests a film ("movie") or seasons of a series ("series";
// none means all of them) as a user, so Seerr applies that user's
// permissions (auto-approval) and quotas.
func (c *Client) RequestTitle(ctx context.Context, userID int, kind string, tmdbID int, seasons []int) (Request, error) {
	body := map[string]any{"mediaId": tmdbID, "is4k": false}
	switch kind {
	case "movie":
		body["mediaType"] = "movie"
	case "series":
		body["mediaType"] = "tv"
		if len(seasons) == 0 {
			body["seasons"] = "all"
		} else {
			body["seasons"] = seasons
		}
	default:
		return Request{}, ErrRefused
	}
	var raw rawRequest
	if err := c.sendAs(ctx, userID, http.MethodPost, "/request", body, &raw); err != nil {
		return Request{}, err
	}
	return raw.request(), nil
}

// UserRequests lists a user's requests, the latest first (100 at most).
func (c *Client) UserRequests(ctx context.Context, userID int) ([]Request, error) {
	var page struct{ Results []rawRequest }
	q := url.Values{"requestedBy": {strconv.Itoa(userID)}, "take": {"100"}, "filter": {"all"}, "sort": {"added"}}
	if err := c.get(ctx, "/request", q, &page); err != nil {
		return nil, err
	}
	out := make([]Request, 0, len(page.Results))
	for _, r := range page.Results {
		out = append(out, r.request())
	}
	return out, nil
}

// maxOpenPages caps how many pages of 100 requests OpenRequests reads.
const maxOpenPages = 50

// OpenRequests lists the requests anyone made that are still to come:
// pending or approved, for a title not available yet. Declined, failed
// and completed ones are left out.
func (c *Client) OpenRequests(ctx context.Context) ([]Request, error) {
	var out []Request
	for page := 0; page < maxOpenPages; page++ {
		var res struct{ Results []rawRequest }
		q := url.Values{"take": {"100"}, "skip": {strconv.Itoa(page * 100)}, "filter": {"all"}, "sort": {"modified"}}
		if err := c.get(ctx, "/request", q, &res); err != nil {
			return nil, err
		}
		for _, raw := range res.Results {
			r := raw.request()
			if (r.Status == 1 || r.Status == 2) && r.MediaStatus != 5 && r.TMDBID > 0 {
				out = append(out, r)
			}
		}
		if len(res.Results) < 100 {
			break
		}
	}
	return out, nil
}

// CancelRequest removes a request (Seerr removes pending ones only, for a
// user without the right to manage requests).
func (c *Client) CancelRequest(ctx context.Context, userID, id int) error {
	return c.sendAs(ctx, userID, http.MethodDelete, "/request/"+strconv.Itoa(id), nil, nil)
}

// DeleteUser deletes a Seerr user (gone already counts as done).
func (c *Client) DeleteUser(ctx context.Context, id int) error {
	err := c.send(ctx, http.MethodDelete, "/user/"+strconv.Itoa(id), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
