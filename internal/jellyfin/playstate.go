package jellyfin

import (
	"context"
	"net/http"
	"net/url"
)

// PlayState is what Overfin reports to Jellyfin about a playback, so its
// sessions and transcodes stay coherent (what people watched is Overfin's).
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

// VideoCopied tells whether Jellyfin copies the video of the item a device
// is playing, rather than re-encoding it, from the device's session; found
// is false while Jellyfin has no transcode for it.
func (c *Client) VideoCopied(ctx context.Context, a Auth, deviceID, itemID string) (copied, found bool, err error) {
	var sessions []struct {
		NowPlayingItem  *struct{ Id string }
		TranscodingInfo *struct{ IsVideoDirect bool }
	}
	if err := c.doQuery(ctx, "sessions", http.MethodGet, "/Sessions", url.Values{"deviceId": {deviceID}}, a, nil, &sessions); err != nil {
		return false, false, err
	}
	for _, s := range sessions {
		if s.NowPlayingItem == nil || s.TranscodingInfo == nil {
			continue
		}
		if id, err := NormalizeID(s.NowPlayingItem.Id); err == nil && id == itemID {
			return s.TranscodingInfo.IsVideoDirect, true, nil
		}
	}
	return false, false, nil
}
