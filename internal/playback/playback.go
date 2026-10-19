// Package playback turns a client's request to play something into a stream
// the client can fetch through Overfin, without ever seeing Jellyfin.
//
// The client describes what it can play (Capabilities); Overfin translates
// that into Jellyfin's device profile, lets Jellyfin decide how to serve the
// item, checks Jellyfin's answer, and hands the client a stream ticket: a
// random value that maps, inside Overfin only, to the real upstream stream
// (SECURITY.md rules 4, 5 and 30).
package playback

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/C0PT3R/overfin/internal/ids"
	"github.com/C0PT3R/overfin/internal/jellyfin"
)

// ---- Capabilities -------------------------------------------------------------

// Capabilities is what the playing device can handle, in the API's simple
// form.
type Capabilities struct {
	Containers       []string `json:"containers"`
	VideoCodecs      []string `json:"videoCodecs"`
	AudioCodecs      []string `json:"audioCodecs"`
	MaxHeight        int      `json:"maxHeight"`
	HDR              []string `json:"hdr"`
	Subtitles        []string `json:"subtitles"`
	MaxAudioChannels int      `json:"maxAudioChannels"`
	MaxBitrateKbps   int      `json:"maxBitrateKbps"`
}

// Known values. Unknown ones are ignored, as the API promises, so a client can
// declare a new codec without breaking anything; nothing else reaches
// Jellyfin.
var (
	knownContainers  = []string{"mp4", "m4v", "mov", "webm", "mkv", "ts", "mp3", "aac", "m4a", "flac", "ogg", "opus", "wav"}
	knownVideoCodecs = []string{"h264", "hevc", "av1", "vp8", "vp9"}
	knownAudioCodecs = []string{"aac", "mp3", "opus", "vorbis", "flac", "ac3", "eac3", "alac", "pcm_s16le"}
	audioContainers  = []string{"mp3", "aac", "m4a", "flac", "ogg", "opus", "wav", "webm", "mp4"}
)

func known(values, allowed []string) []string {
	var out []string
	for _, v := range values {
		v = strings.ToLower(v)
		if slices.Contains(allowed, v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// DeviceProfile translates capabilities into Jellyfin's device profile.
// Whatever the client claims, transcoding to H.264 and AAC in HLS stays
// available as the fallback every browser can play.
func DeviceProfile(c Capabilities, maxBitrate int) jellyfin.DeviceProfile {
	containers := known(c.Containers, knownContainers)
	video := known(c.VideoCodecs, knownVideoCodecs)
	audio := known(c.AudioCodecs, knownAudioCodecs)
	if len(audio) == 0 {
		audio = []string{"aac"}
	}
	channels := "2"
	if c.MaxAudioChannels >= 1 && c.MaxAudioChannels <= 8 {
		channels = strconv.Itoa(c.MaxAudioChannels)
	}

	p := jellyfin.DeviceProfile{
		Name:                "Overfin",
		MaxStreamingBitrate: maxBitrate,
		MaxStaticBitrate:    maxBitrate,
	}
	if len(containers) > 0 && len(video) > 0 {
		p.DirectPlayProfiles = append(p.DirectPlayProfiles, jellyfin.DirectPlayProfile{
			Container: strings.Join(containers, ","), Type: "Video",
			VideoCodec: strings.Join(video, ","), AudioCodec: strings.Join(audio, ","),
		})
	}
	if ac := known(containers, audioContainers); len(ac) > 0 {
		p.DirectPlayProfiles = append(p.DirectPlayProfiles, jellyfin.DirectPlayProfile{
			Container: strings.Join(ac, ","), Type: "Audio", AudioCodec: strings.Join(audio, ","),
		})
	}

	// The transcode target: H.264 always, HEVC too when the client says so.
	transcodeVideo := "h264"
	if slices.Contains(video, "hevc") {
		transcodeVideo = "h264,hevc"
	}
	p.TranscodingProfiles = []jellyfin.TranscodingProfile{
		{Container: "mp4", Type: "Video", VideoCodec: transcodeVideo, AudioCodec: "aac", Protocol: "hls",
			Context: "Streaming", MaxAudioChannels: channels, MinSegments: 1, BreakOnNonKeyFrames: true},
		{Container: "mp4", Type: "Audio", AudioCodec: "aac", Protocol: "hls",
			Context: "Streaming", MaxAudioChannels: channels, MinSegments: 1, BreakOnNonKeyFrames: true},
	}
	if c.MaxHeight >= 144 && c.MaxHeight <= 4320 {
		p.CodecProfiles = append(p.CodecProfiles, jellyfin.CodecProfile{
			Type: "Video",
			Conditions: []jellyfin.ProfileCondition{
				{Condition: "LessThanEqual", Property: "Height", Value: strconv.Itoa(c.MaxHeight)},
			},
		})
	}
	// Subtitles reach clients only as WebVTT (rule 11); anything else, such
	// as image-based subtitles, is burned into the video.
	p.SubtitleProfiles = []jellyfin.SubtitleProfile{
		{Format: "vtt", Method: "External"},
		{Format: "srt", Method: "External"},
		{Format: "ass", Method: "External"},
		{Format: "ssa", Method: "External"},
		{Format: "subrip", Method: "External"},
		{Format: "pgssub", Method: "Encode"},
		{Format: "dvdsub", Method: "Encode"},
		{Format: "dvbsub", Method: "Encode"},
	}
	return p
}

// ---- Choosing how to play -----------------------------------------------------

var (
	// ErrNotPlayable: Jellyfin offered no way to play the item, or only
	// remote sources, which Overfin refuses (rule 5).
	ErrNotPlayable = errors.New("playback: not playable")
	// ErrUnsafeUpstream: Jellyfin's stream URL isn't one of its known
	// streaming routes.
	ErrUnsafeUpstream = errors.New("playback: unexpected upstream stream URL")
)

// Upstream is how Overfin fetches the stream from Jellyfin. It never leaves
// the server: its query carries Jellyfin's token.
type Upstream struct {
	Path  string
	Query url.Values
}

// transcodePath matches the relative stream URLs Jellyfin generates for
// HLS: /videos/{id}/master.m3u8 or /audio/{id}/main.m3u8 and the like.
var transcodePath = regexp.MustCompile(`(?i)^/(videos|audio)/[0-9a-f]{32}/(master|main)\.m3u8$`)

// playSession checks Jellyfin's play session ID like any upstream value
// (rule 7) before it is sent back to Jellyfin.
var playSession = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

// Decision is how one playback will be served.
type Decision struct {
	Method   string // "direct", "remux" or "transcode"
	Protocol string // "file" or "hls"
	Source   jellyfin.MediaSource
	Upstream Upstream
}

// Decide picks how to play from Jellyfin's answer. For direct play, Overfin
// builds the upstream URL itself; for HLS it accepts Jellyfin's URL only as a
// relative path on a known streaming route (rule 5).
func Decide(info *jellyfin.PlaybackInfo, itemID string, audioItem bool, deviceID string) (*Decision, error) {
	if info.ErrorCode != "" || len(info.MediaSources) == 0 {
		return nil, ErrNotPlayable
	}
	src := info.MediaSources[0]
	if src.IsRemote || !strings.EqualFold(src.Protocol, "File") {
		return nil, ErrNotPlayable
	}
	sourceID, err := jellyfin.NormalizeID(src.Id)
	if err != nil || !playSession.MatchString(info.PlaySessionId) {
		return nil, ErrNotPlayable
	}

	if src.SupportsDirectPlay {
		route := "/Videos/"
		if audioItem {
			route = "/Audio/"
		}
		return &Decision{
			Method: "direct", Protocol: "file", Source: src,
			Upstream: Upstream{Path: route + itemID + "/stream", Query: url.Values{
				"static":        {"true"},
				"mediaSourceId": {sourceID},
				"playSessionId": {info.PlaySessionId},
				"deviceId":      {deviceID},
			}},
		}, nil
	}

	if src.TranscodingUrl == "" || !strings.EqualFold(src.TranscodingSubProtocol, "hls") {
		return nil, ErrNotPlayable
	}
	u, err := url.Parse(src.TranscodingUrl)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" ||
		!transcodePath.MatchString(u.Path) {
		return nil, ErrUnsafeUpstream
	}
	method := "transcode"
	if src.SupportsDirectStream {
		method = "remux"
	}
	return &Decision{Method: method, Protocol: "hls", Source: src,
		Upstream: Upstream{Path: u.Path, Query: u.Query()}}, nil
}

// ---- Tickets ----------------------------------------------------------------------

// Stream is one active playback, as Overfin keeps it.
type Stream struct {
	ID            string // play_…, the playback session's public ID
	Ticket        string // secret, in stream URLs only
	ProfileID     string
	ItemID        string // Jellyfin ID
	SourceID      string
	PlaySessionID string
	Decision      Decision
	Expires       time.Time
}

// Tickets holds active streams in memory. Playback state is short-lived and
// never written to disk (rule 40).
//
// TODO: bind tickets to the browser session as well (rule 30) once sessions
// exist; for now they are bound to the profile.
type Tickets struct {
	mu       sync.Mutex
	byTicket map[string]*Stream
	byID     map[string]*Stream
	TTL      time.Duration
}

// NewTickets returns an empty store.
func NewTickets(ttl time.Duration) *Tickets {
	return &Tickets{byTicket: map[string]*Stream{}, byID: map[string]*Stream{}, TTL: ttl}
}

// Add registers a stream and gives it its public ID and secret ticket.
func (t *Tickets) Add(s *Stream, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	s.ID = ids.New("play")
	s.Ticket = random(24)
	s.Expires = now.Add(t.TTL)
	t.byTicket[s.Ticket] = s
	t.byID[s.ID] = s
}

// ByTicket finds a live stream by its ticket.
func (t *Tickets) ByTicket(ticket string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byTicket[ticket]
	if !ok || !now.Before(s.Expires) {
		return nil, false
	}
	return s, true
}

// ByID finds a live stream by its public ID, only for the profile that owns
// it (rule 23).
func (t *Tickets) ByID(id, profileID string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byID[id]
	if !ok || s.ProfileID != profileID || !now.Before(s.Expires) {
		return nil, false
	}
	return s, true
}

func (t *Tickets) prune(now time.Time) {
	for k, s := range t.byTicket {
		if !now.Before(s.Expires) {
			delete(t.byTicket, k)
			delete(t.byID, s.ID)
		}
	}
}

// random returns n random bytes as URL-safe text. Tickets get 24 bytes, well
// above the 128-bit minimum (rule 23).
func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
