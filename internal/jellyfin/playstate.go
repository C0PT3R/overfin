package jellyfin

import (
	"context"
	"net/http"
	"net/url"
)

// PlayState is what Overfin reports to Jellyfin about a playback, so watch
// history and "continue watching" stay correct.
type PlayState struct {
	ItemId              string
	MediaSourceId       string
	PlaySessionId       string
	PositionTicks       int64
	IsPaused            bool
	CanSeek             bool
	PlayMethod          string // "DirectPlay", "DirectStream" or "Transcode"
	AudioStreamIndex    *int   `json:",omitempty"`
	SubtitleStreamIndex *int   `json:",omitempty"`
}

// ReportStart tells Jellyfin a playback began.
func (c *Client) ReportStart(ctx context.Context, a Auth, s PlayState) error {
	return c.do(ctx, "report start", http.MethodPost, "/Sessions/Playing", a, s, nil)
}

// ReportProgress tells Jellyfin where a playback is.
func (c *Client) ReportProgress(ctx context.Context, a Auth, s PlayState) error {
	return c.do(ctx, "report progress", http.MethodPost, "/Sessions/Playing/Progress", a, s, nil)
}

// ReportStopped tells Jellyfin a playback ended, and where.
func (c *Client) ReportStopped(ctx context.Context, a Auth, s PlayState) error {
	body := map[string]any{
		"ItemId": s.ItemId, "MediaSourceId": s.MediaSourceId,
		"PlaySessionId": s.PlaySessionId, "PositionTicks": s.PositionTicks,
	}
	return c.do(ctx, "report stopped", http.MethodPost, "/Sessions/Playing/Stopped", a, body, nil)
}

// StopEncoding ends the transcode behind a playback. Jellyfin doesn't stop
// abandoned transcodes by itself, so Overfin always calls this when a
// playback ends (SECURITY.md rule 33).
func (c *Client) StopEncoding(ctx context.Context, a Auth, deviceID, playSessionID string) error {
	return c.doQuery(ctx, "stop encoding", http.MethodDelete, "/Videos/ActiveEncodings",
		url.Values{"deviceId": {deviceID}, "playSessionId": {playSessionID}}, a, nil, nil)
}
