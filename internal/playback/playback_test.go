package playback

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/C0PT3R/overfin/internal/jellyfin"
)

const (
	item   = "0123456789abcdef0123456789abcdef"
	source = "fedcba9876543210fedcba9876543210"
)

func TestDeviceProfile(t *testing.T) {
	p := DeviceProfile(Capabilities{
		Containers:  []string{"mp4", "WEBM", "evil;container", "mkv"},
		VideoCodecs: []string{"h264", "hevc", "made-up"},
		AudioCodecs: []string{"aac", "opus"},
		MaxHeight:   1080,
	}, 20_000_000)

	out, _ := json.Marshal(p)
	if strings.Contains(string(out), "evil") || strings.Contains(string(out), "made-up") {
		t.Errorf("unknown values reached the profile: %s", out)
	}
	if len(p.DirectPlayProfiles) == 0 || p.DirectPlayProfiles[0].Container != "mp4,webm,mkv" {
		t.Errorf("direct play: %+v", p.DirectPlayProfiles)
	}
	if p.TranscodingProfiles[0].VideoCodec != "h264,hevc" || p.TranscodingProfiles[0].Protocol != "hls" {
		t.Errorf("transcoding: %+v", p.TranscodingProfiles[0])
	}
	if len(p.CodecProfiles) != 1 || p.CodecProfiles[0].Conditions[0].Value != "1080" {
		t.Errorf("codec profiles: %+v", p.CodecProfiles)
	}

	// A client that claims nothing still gets the H.264 fallback, and no
	// list is null (Jellyfin answers 400 to that).
	bare := DeviceProfile(Capabilities{}, 1)
	if raw, _ := json.Marshal(bare); strings.Contains(string(raw), "null") {
		t.Errorf("null in profile: %s", raw)
	}
	if len(bare.DirectPlayProfiles) != 0 || bare.TranscodingProfiles[0].VideoCodec != "h264" {
		t.Errorf("bare profile: %+v", bare)
	}
}

func info(src jellyfin.MediaSource) *jellyfin.PlaybackInfo {
	if src.Id == "" {
		src.Id = source
	}
	if src.Protocol == "" {
		src.Protocol = "File"
	}
	return &jellyfin.PlaybackInfo{MediaSources: []jellyfin.MediaSource{src}, PlaySessionId: "ps1"}
}

func TestDecide(t *testing.T) {
	d, err := Decide(info(jellyfin.MediaSource{SupportsDirectPlay: true}), item, false, "dev")
	if err != nil || d.Method != "direct" || d.Upstream.Path != "/Videos/"+item+"/stream" || d.Upstream.Query.Get("static") != "true" {
		t.Errorf("direct play: %+v, %v", d, err)
	}

	hls := "/videos/" + item + "/master.m3u8?MediaSourceId=" + source + "&ApiKey=secret-token"
	d, err = Decide(info(jellyfin.MediaSource{TranscodingUrl: hls, TranscodingSubProtocol: "hls"}), item, false, "dev")
	if err != nil || d.Method != "transcode" || d.Protocol != "hls" || d.Upstream.Query.Get("ApiKey") != "secret-token" {
		t.Errorf("transcode: %+v, %v", d, err)
	}
	// Jellyfin 12 writes the ID with dashes.
	dashed := "/videos/01234567-89ab-cdef-0123-456789abcdef/master.m3u8?ApiKey=x"
	if d, err := Decide(info(jellyfin.MediaSource{TranscodingUrl: dashed, TranscodingSubProtocol: "hls"}), item, false, "dev"); err != nil {
		t.Errorf("dashed ID: %+v, %v", d, err)
	}
	d, err = Decide(info(jellyfin.MediaSource{TranscodingUrl: hls, TranscodingSubProtocol: "hls", SupportsDirectStream: true}), item, false, "dev")
	if err != nil || d.Method != "remux" {
		t.Errorf("remux: %+v, %v", d, err)
	}
}

func TestDecideRefuses(t *testing.T) {
	tests := map[string]struct {
		src  jellyfin.MediaSource
		want error
	}{
		"remote source":         {jellyfin.MediaSource{SupportsDirectPlay: true, IsRemote: true}, ErrNotPlayable},
		"http protocol (.strm)": {jellyfin.MediaSource{SupportsDirectPlay: true, Protocol: "Http"}, ErrNotPlayable},
		"absolute URL": {jellyfin.MediaSource{TranscodingSubProtocol: "hls",
			TranscodingUrl: "http://198.51.100.1/videos/" + item + "/master.m3u8"}, ErrUnsafeUpstream},
		"protocol-relative URL": {jellyfin.MediaSource{TranscodingSubProtocol: "hls",
			TranscodingUrl: "//198.51.100.1/videos/" + item + "/master.m3u8"}, ErrUnsafeUpstream},
		"unknown route": {jellyfin.MediaSource{TranscodingSubProtocol: "hls",
			TranscodingUrl: "/System/Info"}, ErrUnsafeUpstream},
		"path traversal": {jellyfin.MediaSource{TranscodingSubProtocol: "hls",
			TranscodingUrl: "/videos/" + item + "/../../System/master.m3u8"}, ErrUnsafeUpstream},
		"another item's stream": {jellyfin.MediaSource{TranscodingSubProtocol: "hls",
			TranscodingUrl: "/videos/" + source + "/master.m3u8"}, ErrUnsafeUpstream},
		"not hls": {jellyfin.MediaSource{TranscodingSubProtocol: "http",
			TranscodingUrl: "/videos/" + item + "/stream.mp4"}, ErrNotPlayable},
		"bad source ID": {jellyfin.MediaSource{SupportsDirectPlay: true, Id: "../x"}, ErrNotPlayable},
	}
	for name, tt := range tests {
		if _, err := Decide(info(tt.src), item, false, "dev"); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tt.want)
		}
	}
	if _, err := Decide(&jellyfin.PlaybackInfo{ErrorCode: "NotAllowed"}, item, false, "dev"); !errors.Is(err, ErrNotPlayable) {
		t.Errorf("error code: %v", err)
	}
}

func TestTickets(t *testing.T) {
	tk := NewTickets(time.Hour)
	now := time.Now()
	s := &Stream{ProfileID: "prof_a", ItemID: item}
	tk.Add(s, now)

	if len(s.Ticket) < 32 || !strings.HasPrefix(s.ID, "play_") {
		t.Errorf("ticket %q, id %q", s.Ticket, s.ID)
	}
	if got, ok := tk.ByTicket(s.Ticket, now); !ok || got != s {
		t.Error("ticket not found")
	}
	if _, ok := tk.ByID(s.ID, "prof_b", now); ok {
		t.Error("another profile found the playback")
	}
	if _, ok := tk.ByTicket(s.Ticket, now.Add(2*time.Hour)); ok {
		t.Error("expired ticket still works")
	}
}

func TestDescribe(t *testing.T) {
	src := jellyfin.MediaSource{Name: "Film.2160p.x265-GROUP", MediaStreams: []jellyfin.MediaStream{
		{Type: "Video", Height: 2160, VideoRange: "HDR", Codec: "hevc"},
		{Type: "Audio", Codec: "aac"},
	}}
	if got := Describe(src); got != "2160p HDR · HEVC" {
		t.Errorf("Describe = %q", got)
	}
	if got := Describe(jellyfin.MediaSource{}); got != "Version" {
		t.Errorf("no streams: %q", got)
	}
}
