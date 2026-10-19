package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// MaxImageBytes bounds an image read from Jellyfin.
const MaxImageBytes = 15 << 20 // 15 MiB

// ErrImageTooLarge is returned when Jellyfin sends more than MaxImageBytes.
var ErrImageTooLarge = errors.New("jellyfin: image too large")

// ImageRequest describes one image. Every field is validated by the caller:
// Type is one of Jellyfin's image types, Tag and ItemID are hex.
type ImageRequest struct {
	ItemID   string
	Type     string // "Primary", "Backdrop", "Logo", "Thumb", "Chapter"
	Index    int
	Tag      string
	MaxWidth int
	Format   string // "Jpg" or "Png"
}

// Image fetches one image's bytes. Jellyfin resizes it to MaxWidth.
func (c *Client) Image(ctx context.Context, a Auth, req ImageRequest) ([]byte, error) {
	id, err := NormalizeID(req.ItemID)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path += "/Items/" + id + "/Images/" + req.Type + "/" + strconv.Itoa(req.Index)
	u.RawQuery = url.Values{
		"tag":      {req.Tag},
		"maxWidth": {strconv.Itoa(req.MaxWidth)},
		"format":   {req.Format},
		"quality":  {"90"},
	}.Encode()

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", c.header(a))
	res, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: image: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, &StatusError{Op: "image", Status: res.StatusCode}
	}
	// Read one byte more than allowed, to tell "exactly the limit" from
	// "over it".
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jellyfin: image: %w", err)
	}
	if len(b) > MaxImageBytes {
		return nil, ErrImageTooLarge
	}
	return b, nil
}
