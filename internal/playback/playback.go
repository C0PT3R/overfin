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
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/textclean"
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
	knownHDR         = []string{"hdr10", "hlg"}
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

	// Lists start empty, never nil: Go encodes a nil slice as null, and
	// Jellyfin refuses null where it expects a list.
	p := jellyfin.DeviceProfile{
		Name:                "Overfin",
		MaxStreamingBitrate: maxBitrate,
		MaxStaticBitrate:    maxBitrate,
		DirectPlayProfiles:  []jellyfin.DirectPlayProfile{},
		CodecProfiles:       []jellyfin.CodecProfile{},
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
	// The color ranges the screen shows, for the codecs that carry HDR:
	// without them, Jellyfin takes the screen for standard range only and
	// converts HDR, which means re-encoding the video. HDR10+ carries HDR10
	// as its base. So does Dolby Vision profile 8 (most 4K releases): its
	// HDR10, HLG or SDR base plays as such, Jellyfin removing the Dolby
	// Vision layer where a player needs it; its copy costs nothing. Dolby
	// Vision alone (profile 5) has no such base and is converted.
	ranges := "SDR|DOVIWithSDR"
	hdr := known(c.HDR, knownHDR)
	if slices.Contains(hdr, "hdr10") {
		ranges += "|HDR10|HDR10Plus|DOVIWithHDR10|DOVIWithHDR10Plus|DOVIWithEL|DOVIWithELHDR10Plus"
	}
	if slices.Contains(hdr, "hlg") {
		ranges += "|HLG|DOVIWithHLG"
	}
	for _, codec := range []string{"hevc", "av1", "vp9"} {
		if slices.Contains(video, codec) {
			p.CodecProfiles = append(p.CodecProfiles, jellyfin.CodecProfile{
				Type: "Video", Codec: codec,
				Conditions: []jellyfin.ProfileCondition{
					{Condition: "EqualsAny", Property: "VideoRangeType", Value: ranges},
				},
			})
		}
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
// HLS: /videos/{id}/master.m3u8 or /audio/{id}/main.m3u8 and the like. The
// ID may be written with or without dashes.
var transcodePath = regexp.MustCompile(`(?i)^/(videos|audio)/([0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12})/(master|main)\.m3u8$`)

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

// Decide picks how to play from Jellyfin's answer. When it offers several
// versions, the one that costs the server least wins: direct play, then
// remux, then transcode; between equals, Jellyfin's order. For direct play,
// Overfin builds the upstream URL itself; for HLS it accepts Jellyfin's URL
// only as a relative path on a known streaming route (rule 5).
func Decide(info *jellyfin.PlaybackInfo, itemID string, audioItem bool, deviceID string) (*Decision, error) {
	if info.ErrorCode != "" || !playSession.MatchString(info.PlaySessionId) {
		return nil, ErrNotPlayable
	}
	var best *Decision
	for _, src := range info.MediaSources {
		d, err := decideSource(src, info.PlaySessionId, itemID, audioItem, deviceID)
		if errors.Is(err, ErrNotPlayable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if best == nil || cost[d.Method] < cost[best.Method] {
			best = d
		}
	}
	if best == nil {
		return nil, ErrNotPlayable
	}
	return best, nil
}

// cost orders the methods by what they cost the server.
var cost = map[string]int{"direct": 0, "remux": 1, "transcode": 2}

func decideSource(src jellyfin.MediaSource, playSessionID, itemID string, audioItem bool, deviceID string) (*Decision, error) {
	if src.IsRemote || !strings.EqualFold(src.Protocol, "File") {
		return nil, ErrNotPlayable
	}
	sourceID, err := jellyfin.NormalizeID(src.Id)
	if err != nil {
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
				"playSessionId": {playSessionID},
				"deviceId":      {deviceID},
			}},
		}, nil
	}

	if src.TranscodingUrl == "" || !strings.EqualFold(src.TranscodingSubProtocol, "hls") {
		return nil, ErrNotPlayable
	}
	u, err := url.Parse(src.TranscodingUrl)
	if err != nil {
		return nil, ErrUnsafeUpstream
	}
	m := transcodePath.FindStringSubmatch(u.Path)
	var pathItem string
	if m != nil {
		pathItem, _ = jellyfin.NormalizeID(m[2])
	}
	// The URL must also be for the item that was asked for.
	if u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || m == nil || pathItem != itemID {
		// The path is safe to report; the query carries Jellyfin's token
		// and never is.
		return nil, fmt.Errorf("%w: path %q", ErrUnsafeUpstream, textclean.Plain(u.Path, 200))
	}
	method := "transcode"
	if keepsMainStream(u.Query(), audioItem) {
		method = "remux"
	}
	return &Decision{Method: method, Protocol: "hls", Source: src,
		Upstream: Upstream{Path: u.Path, Query: u.Query()}}, nil
}

// Jellyfin's reasons for an HLS stream that leave the video as it is: only
// the container or the audio changes. For a music track or an audiobook,
// only a new container leaves it as it is.
var (
	videoKeptReasons = []string{
		"ContainerNotSupported", "AudioCodecNotSupported", "AudioIsExternal",
		"SecondaryAudioNotSupported", "AudioChannelsNotSupported", "AudioProfileNotSupported",
		"AudioSampleRateNotSupported", "AudioBitDepthNotSupported", "AudioBitrateNotSupported",
		"UnknownAudioStreamInfo",
	}
	audioKeptReasons = []string{"ContainerNotSupported"}
)

// keepsMainStream says whether an HLS stream only repackages the item's
// video (or, for audio items, its audio) instead of re-encoding it. Jellyfin
// tells why it streams over HLS only in the stream URL's query. Anything
// unclear counts as re-encoding, so limits on transcodes are never too
// loose.
func keepsMainStream(q url.Values, audioItem bool) bool {
	kept := videoKeptReasons
	if audioItem {
		kept = audioKeptReasons
	} else if slices.ContainsFunc(param(q, "AllowVideoStreamCopy"), isFalse) {
		return false
	}
	// Subtitles burned into the video. Jellyfin also writes
	// SubtitleMethod=Encode without a subtitle track, when none is shown:
	// nothing is burned then.
	if slices.ContainsFunc(param(q, "SubtitleMethod"), isEncode) &&
		slices.ContainsFunc(param(q, "SubtitleStreamIndex"), func(v string) bool { n, err := strconv.Atoi(v); return err == nil && n >= 0 }) {
		return false
	}
	var reasons []string
	for _, v := range param(q, "TranscodeReasons") {
		for r := range strings.SplitSeq(v, ",") {
			reasons = append(reasons, strings.TrimSpace(r))
		}
	}
	if len(reasons) == 0 {
		return false
	}
	for _, r := range reasons {
		if !slices.ContainsFunc(kept, func(k string) bool { return strings.EqualFold(k, r) }) {
			return false
		}
	}
	return true
}

// Reasons are Jellyfin's reasons for a stream it converts (none for a
// file played as it is), for the logs and the player's information.
func (d *Decision) Reasons() []string {
	var out []string
	for _, v := range param(d.Upstream.Query, "TranscodeReasons") {
		for r := range strings.SplitSeq(v, ",") {
			if r = strings.TrimSpace(r); r != "" {
				out = append(out, r)
			}
		}
	}
	return out
}

// param returns a query parameter's values whatever its case, as Jellyfin
// reads its own queries.
func param(q url.Values, name string) []string {
	var out []string
	for k, vs := range q {
		if strings.EqualFold(k, name) {
			out = append(out, vs...)
		}
	}
	return out
}

func isFalse(v string) bool  { return strings.EqualFold(v, "false") }
func isEncode(v string) bool { return strings.EqualFold(v, "encode") }

// Describe gives a version a neutral name from its streams, such as
// "2160p HDR · HEVC". Jellyfin's own version names come from file names,
// which may reveal how files were named; those are shown only when the
// operator exposes them (rule 55).
func Describe(src jellyfin.MediaSource) string {
	var parts []string
	for _, ms := range src.MediaStreams {
		if ms.Type != "Video" {
			continue
		}
		res := resolution(ms.Width, ms.Height)
		if strings.EqualFold(ms.VideoRange, "HDR") {
			res = strings.TrimSpace(res + " HDR")
		}
		if res != "" {
			parts = append(parts, res)
		}
		if c := codecName(ms.Codec); c != "" {
			parts = append(parts, c)
		}
		break
	}
	if len(parts) == 0 {
		return "Version"
	}
	return strings.Join(parts, " · ")
}

// resolution names a picture size by its width first: films are wide, and
// a 4K film at 2.39:1 is 3840x1600, which its height alone would call
// 1080p.
func resolution(w, h int) string {
	switch {
	case w >= 3200 || h >= 2000:
		return "2160p"
	case w >= 1800 || h >= 1000:
		return "1080p"
	case w >= 1200 || h >= 700:
		return "720p"
	case w > 0 || h > 0:
		return "SD"
	}
	return ""
}

func codecName(c string) string {
	switch strings.ToLower(c) {
	case "hevc", "h265":
		return "HEVC"
	case "h264", "avc":
		return "H.264"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "mpeg2video":
		return "MPEG-2"
	}
	return ""
}
