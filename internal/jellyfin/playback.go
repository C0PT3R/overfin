package jellyfin

import (
	"context"
	"net/http"
	"net/url"
)

// DeviceProfile tells Jellyfin what a client can play. Overfin builds it
// from the client's simple capabilities; clients never send one directly.
type DeviceProfile struct {
	Name                string               `json:"Name"`
	MaxStreamingBitrate int                  `json:"MaxStreamingBitrate"`
	MaxStaticBitrate    int                  `json:"MaxStaticBitrate"`
	DirectPlayProfiles  []DirectPlayProfile  `json:"DirectPlayProfiles"`
	TranscodingProfiles []TranscodingProfile `json:"TranscodingProfiles"`
	CodecProfiles       []CodecProfile       `json:"CodecProfiles"`
	SubtitleProfiles    []SubtitleProfile    `json:"SubtitleProfiles"`
}

type DirectPlayProfile struct {
	Container  string `json:"Container"`
	Type       string `json:"Type"`
	VideoCodec string `json:"VideoCodec,omitempty"`
	AudioCodec string `json:"AudioCodec,omitempty"`
}

type TranscodingProfile struct {
	Container           string `json:"Container"`
	Type                string `json:"Type"`
	VideoCodec          string `json:"VideoCodec,omitempty"`
	AudioCodec          string `json:"AudioCodec"`
	Protocol            string `json:"Protocol"`
	Context             string `json:"Context"`
	MaxAudioChannels    string `json:"MaxAudioChannels,omitempty"`
	MinSegments         int    `json:"MinSegments,omitempty"`
	BreakOnNonKeyFrames bool   `json:"BreakOnNonKeyFrames"`
}

type CodecProfile struct {
	Type       string             `json:"Type"`
	Codec      string             `json:"Codec,omitempty"`
	Conditions []ProfileCondition `json:"Conditions"`
}

type ProfileCondition struct {
	Condition  string `json:"Condition"`
	Property   string `json:"Property"`
	Value      string `json:"Value"`
	IsRequired bool   `json:"IsRequired"`
}

type SubtitleProfile struct {
	Format string `json:"Format"`
	Method string `json:"Method"`
}

// PlaybackRequest is what Overfin asks Jellyfin for one playback.
type PlaybackRequest struct {
	MediaSourceID       string
	StartTimeTicks      int64
	AudioStreamIndex    *int
	SubtitleStreamIndex *int
	MaxStreamingBitrate int
	DeviceProfile       DeviceProfile
}

// MediaSource holds what Overfin reads of one version of an item. Jellyfin
// also sends the file's path, the encoder's path and the headers needed to
// reach remote sources; none of it is decoded.
type MediaSource struct {
	Id                         string
	Name                       string
	Container                  string
	Protocol                   string
	IsRemote                   bool
	RunTimeTicks               int64
	SupportsDirectPlay         bool
	SupportsDirectStream       bool
	SupportsTranscoding        bool
	TranscodingUrl             string
	TranscodingSubProtocol     string
	DefaultAudioStreamIndex    *int
	DefaultSubtitleStreamIndex *int
	MediaStreams               []MediaStream
}

type MediaStream struct {
	Index                int
	Type                 string
	Codec                string
	Language             string
	DisplayTitle         string
	Channels             int
	IsDefault            bool
	IsForced             bool
	IsHearingImpaired    bool
	IsTextSubtitleStream bool
	Height               int
	VideoRange           string
}

// PlaybackInfo is Jellyfin's answer: the versions it can serve to this
// device, and how.
type PlaybackInfo struct {
	MediaSources  []MediaSource
	PlaySessionId string
	ErrorCode     string
}

// PlaybackInfo asks Jellyfin how an item can be played for this device.
// Jellyfin answers 404 when the user may not see the item.
func (c *Client) PlaybackInfo(ctx context.Context, a Auth, userID, itemID string, req PlaybackRequest) (*PlaybackInfo, error) {
	id, err := NormalizeID(itemID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"UserId":               userID,
		"MaxStreamingBitrate":  req.MaxStreamingBitrate,
		"StartTimeTicks":       req.StartTimeTicks,
		"DeviceProfile":        req.DeviceProfile,
		"EnableDirectPlay":     true,
		"EnableDirectStream":   true,
		"EnableTranscoding":    true,
		"AllowVideoStreamCopy": true,
		"AllowAudioStreamCopy": true,
		"AutoOpenLiveStream":   false,
	}
	if req.MediaSourceID != "" {
		body["MediaSourceId"] = req.MediaSourceID
	}
	if req.AudioStreamIndex != nil {
		body["AudioStreamIndex"] = *req.AudioStreamIndex
	}
	if req.SubtitleStreamIndex != nil {
		body["SubtitleStreamIndex"] = *req.SubtitleStreamIndex
	}
	var out PlaybackInfo
	err = c.doQuery(ctx, "playback info", http.MethodPost, "/Items/"+id+"/PlaybackInfo",
		url.Values{"userId": {userID}}, a, body, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Segment is a marked part of an item, such as its intro.
type Segment struct {
	Type       string
	StartTicks int64
	EndTicks   int64
}

// MediaSegments lists an item's marked parts.
func (c *Client) MediaSegments(ctx context.Context, a Auth, itemID string) ([]Segment, error) {
	id, err := NormalizeID(itemID)
	if err != nil {
		return nil, err
	}
	var out struct{ Items []Segment }
	if err := c.get(ctx, "media segments", "/MediaSegments/"+id, a, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
