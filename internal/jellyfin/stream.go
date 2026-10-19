package jellyfin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// streamClient fetches media. Unlike the API client, it has no overall
// timeout, which would cut every film after a few seconds; it only limits
// how long Jellyfin may take to start answering. It never follows redirects.
var streamClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 60 * time.Second, // transcodes can take a moment to start
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Open starts fetching a stream: a media file, a playlist or a segment. path
// and query come from Overfin's own stream state, never from the client;
// rangeHeader is the client's Range header, already checked. The caller
// closes the body.
func (c *Client) Open(ctx context.Context, a Auth, path string, query url.Values, rangeHeader string) (*http.Response, error) {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.header(a))
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	res, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: stream: %w", err)
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
		return res, nil
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	return nil, &StatusError{Op: "stream", Status: res.StatusCode}
}

// ReadSmall fetches a small text resource (a playlist or subtitles) whole,
// refusing anything larger than max bytes.
func (c *Client) ReadSmall(ctx context.Context, a Auth, path string, query url.Values, max int) ([]byte, error) {
	res, err := c.Open(ctx, a, path, query, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &StatusError{Op: "stream", Status: res.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("jellyfin: stream: response larger than %d bytes", max)
	}
	return b, nil
}
