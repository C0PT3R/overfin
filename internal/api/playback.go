package api

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/library"
	"github.com/C0PT3R/overfin/internal/playback"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/textclean"
)

// Playback starts streams. The stream endpoints themselves come next.
type Playback struct {
	Library        Library // for the active profile and Jellyfin access
	Tickets        *playback.Tickets
	MaxBitrateKbps int // the operator's cap; 0 means none
	Logger         *slog.Logger
}

func (Playback) Name() string { return "playback" }

func (pb Playback) Register(r *plugin.Registrar) error {
	return r.HandleFunc("POST /api/v1/playback", pb.start)
}

type startRequest struct {
	ItemID          string                `json:"itemId"`
	Capabilities    playback.Capabilities `json:"capabilities"`
	StartPositionMs int64                 `json:"startPositionMs"`
	VersionID       string                `json:"versionId"`
	AudioTrack      string                `json:"audioTrack"`
	SubtitleTrack   string                `json:"subtitleTrack"`
	MaxBitrateKbps  int                   `json:"maxBitrateKbps"`
	Queue           []string              `json:"queue"`
	Device          string                `json:"device"`
}

// Shapes from the API specification.
type trackJSON struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	Language        string `json:"language,omitempty"`
	Codec           string `json:"codec,omitempty"`
	Channels        int    `json:"channels,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearingImpaired,omitempty"`
	URL             string `json:"url,omitempty"`
}

type versionJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type segmentJSON struct {
	Kind    string `json:"kind"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

type streamJSON struct {
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	MimeType string `json:"mimeType,omitempty"`
}

type sessionJSON struct {
	ID             string              `json:"id"`
	Item           library.ItemSummary `json:"item"`
	Method         string              `json:"method"`
	Stream         streamJSON          `json:"stream"`
	PositionMs     int64               `json:"positionMs"`
	DurationMs     int64               `json:"durationMs,omitempty"`
	Versions       []versionJSON       `json:"versions,omitempty"`
	VersionID      string              `json:"versionId,omitempty"`
	AudioTracks    []trackJSON         `json:"audioTracks,omitempty"`
	AudioTrack     string              `json:"audioTrack,omitempty"`
	SubtitleTracks []trackJSON         `json:"subtitleTracks,omitempty"`
	SubtitleTrack  string              `json:"subtitleTrack,omitempty"`
	MaxBitrateKbps int                 `json:"maxBitrateKbps,omitempty"`
	Segments       []segmentJSON       `json:"segments,omitempty"`
}

var (
	trackID  = regexp.MustCompile(`^[as]([0-9]{1,3})$`)
	deviceRe = regexp.MustCompile(`^[\x20-\x7e]{1,64}$`)
)

// trackIndex turns "a3" or "s5" (with the expected letter) into 3 or 5.
func trackIndex(id string, letter byte) (int, bool) {
	m := trackID.FindStringSubmatch(id)
	if m == nil || id[0] != letter {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

func (pb Playback) start(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if !decode(r, &req) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	jfItem, ok := library.Unwrap(library.ItemPrefix, req.ItemID)
	if !ok || req.StartPositionMs < 0 || req.MaxBitrateKbps < 0 || len(req.Queue) > 0 ||
		(req.Device != "" && !deviceRe.MatchString(req.Device)) {
		// Queues arrive with the rest of playback management.
		problem.Send(w, problem.InvalidRequest, "")
		return
	}

	var pr jellyfin.PlaybackRequest
	pr.StartTimeTicks = req.StartPositionMs * 10_000
	if req.VersionID != "" {
		if pr.MediaSourceID, ok = library.Unwrap("ver_", req.VersionID); !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
	}
	if req.AudioTrack != "" {
		n, ok := trackIndex(req.AudioTrack, 'a')
		if !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
		pr.AudioStreamIndex = &n
	}
	switch req.SubtitleTrack {
	case "":
	case "off":
		off := -1
		pr.SubtitleStreamIndex = &off
	default:
		n, ok := trackIndex(req.SubtitleTrack, 's')
		if !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
		pr.SubtitleStreamIndex = &n
	}
	kbps := lowest(req.MaxBitrateKbps, req.Capabilities.MaxBitrateKbps, pb.MaxBitrateKbps)
	if kbps == 0 {
		kbps = 120_000
	}
	pr.MaxStreamingBitrate = kbps * 1000
	pr.DeviceProfile = playback.DeviceProfile(req.Capabilities, pr.MaxStreamingBitrate)

	l := pb.Library
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()

	raw, err := l.Jellyfin.Item(ctx, auth(p), p.JellyfinUserID, jfItem)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	summary, ok := library.Summary(raw)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	info, err := l.Jellyfin.PlaybackInfo(ctx, auth(p), p.JellyfinUserID, jfItem, pr)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	audioItem := summary.Kind == "track" || summary.Kind == "audiobook"
	dec, err := playback.Decide(info, jfItem, audioItem, auth(p).DeviceID)
	switch {
	case errors.Is(err, playback.ErrNotPlayable):
		problem.Send(w, problem.NotPlayable, "")
		return
	case errors.Is(err, playback.ErrUnsafeUpstream):
		pb.Logger.Warn("refused an unexpected stream URL from Jellyfin", "item", req.ItemID)
		problem.Send(w, problem.NotPlayable, "")
		return
	case err != nil:
		l.upstreamError(w, err)
		return
	}

	sourceID, _ := jellyfin.NormalizeID(dec.Source.Id)
	s := &playback.Stream{
		ProfileID: p.ID, ItemID: jfItem, SourceID: sourceID,
		PlaySessionID: info.PlaySessionId, Decision: *dec,
	}
	pb.Tickets.Add(s, time.Now())

	out := sessionJSON{
		ID: s.ID, Item: summary, Method: dec.Method,
		PositionMs: req.StartPositionMs, DurationMs: dec.Source.RunTimeTicks / 10_000,
		VersionID: "ver_" + sourceID, MaxBitrateKbps: kbps,
	}
	base := "/api/v1/stream/" + s.Ticket + "/"
	if dec.Protocol == "hls" {
		out.Stream = streamJSON{URL: base + "master.m3u8", Protocol: "hls", MimeType: "application/vnd.apple.mpegurl"}
	} else {
		out.Stream = streamJSON{URL: base + "file", Protocol: "file"}
	}
	for _, src := range info.MediaSources {
		if id, err := jellyfin.NormalizeID(src.Id); err == nil {
			out.Versions = append(out.Versions, versionJSON{ID: "ver_" + id, Name: textclean.Plain(src.Name, 200)})
		}
	}
	for _, ms := range dec.Source.MediaStreams {
		t := trackJSON{
			Label: textclean.Plain(ms.DisplayTitle, 200), Language: languageTag(ms.Language),
			Codec: textclean.Plain(ms.Codec, 32), Channels: ms.Channels,
			Default: ms.IsDefault, Forced: ms.IsForced, HearingImpaired: ms.IsHearingImpaired,
		}
		switch ms.Type {
		case "Audio":
			t.ID = "a" + strconv.Itoa(ms.Index)
			out.AudioTracks = append(out.AudioTracks, t)
		case "Subtitle":
			t.ID = "s" + strconv.Itoa(ms.Index)
			if ms.IsTextSubtitleStream {
				t.URL = base + "subtitles/" + t.ID // sanitized WebVTT (rule 11)
			}
			out.SubtitleTracks = append(out.SubtitleTracks, t)
		}
	}
	if pr.AudioStreamIndex != nil {
		out.AudioTrack = req.AudioTrack
	} else if i := dec.Source.DefaultAudioStreamIndex; i != nil {
		out.AudioTrack = "a" + strconv.Itoa(*i)
	}
	if pr.SubtitleStreamIndex != nil {
		out.SubtitleTrack = req.SubtitleTrack
	} else if i := dec.Source.DefaultSubtitleStreamIndex; i != nil && *i >= 0 {
		out.SubtitleTrack = "s" + strconv.Itoa(*i)
	}
	// Skip markers are a bonus: playback goes on without them.
	if segs, err := l.Jellyfin.MediaSegments(ctx, auth(p), jfItem); err == nil {
		for _, sg := range segs {
			if kind := strings.ToLower(sg.Type); kind != "unknown" && kind != "" && sg.EndTicks > sg.StartTicks {
				out.Segments = append(out.Segments, segmentJSON{Kind: kind, StartMs: sg.StartTicks / 10_000, EndMs: sg.EndTicks / 10_000})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeOK(w, out)
}

// lowest returns the smallest positive value, or 0 when there is none.
func lowest(values ...int) int {
	m := 0
	for _, v := range values {
		if v > 0 && (m == 0 || v < m) {
			m = v
		}
	}
	return m
}

var languageRe = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$`)

// languageTag keeps well-formed language codes only.
func languageTag(s string) string {
	if len(s) > 35 || !languageRe.MatchString(s) {
		return ""
	}
	return s
}
