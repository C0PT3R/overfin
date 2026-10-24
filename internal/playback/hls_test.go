package playback

import (
	"errors"
	"strings"
	"testing"
)

const vid = "01234567-89ab-cdef-0123-456789abcdef"

var masterUp = Upstream{Path: "/videos/" + vid + "/master.m3u8"}
var variantUp = Upstream{Path: "/videos/" + vid + "/main.m3u8"}

const master = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=8000000,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1920x1080
main.m3u8?DeviceId=d&MediaSourceId=m&ApiKey=SECRET
#EXT-X-MEDIA:TYPE=SUBTITLES,URI="http://198.51.100.1/evil.m3u8"
#EXT-X-I-FRAME-STREAM-INF:URI="iframe.m3u8?ApiKey=SECRET"
# a comment
`

const variant = `#EXTM3U
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-MAP:URI="hls1/main/-1.mp4?ApiKey=SECRET&runtimeTicks=0"
#EXTINF:6.0, nodesc
hls1/main/0.mp4?ApiKey=SECRET&runtimeTicks=0&actualSegmentLengthTicks=60000000
#EXTINF:6.0, nodesc
hls1/main/1.mp4?ApiKey=SECRET&runtimeTicks=60000000&actualSegmentLengthTicks=60000000
#EXT-X-KEY:METHOD=AES-128,URI="https://198.51.100.1/key"
#EXT-X-ENDLIST
`

func TestRewriteMaster(t *testing.T) {
	r, err := RewriteMaster([]byte(master), masterUp)
	if err != nil {
		t.Fatal(err)
	}
	out := string(r.Body)
	for _, leak := range []string{"SECRET", "ApiKey", "198.51.100.1", "iframe", "EXT-X-MEDIA", "comment"} {
		if strings.Contains(out, leak) {
			t.Errorf("output contains %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "\nv0.m3u8\n") || !strings.Contains(out, "RESOLUTION=1920x1080") {
		t.Errorf("output:\n%s", out)
	}
	up := r.Files["v0.m3u8"]
	if up.Path != "/videos/"+vid+"/main.m3u8" || up.Query.Get("ApiKey") != "SECRET" {
		t.Errorf("upstream for v0: %+v", up)
	}
}

func TestRewriteVariant(t *testing.T) {
	r, err := RewriteVariant([]byte(variant), variantUp)
	if err != nil {
		t.Fatal(err)
	}
	out := string(r.Body)
	for _, leak := range []string{"SECRET", "ApiKey", "runtimeTicks", "hls1", "198.51.100.1", "EXT-X-KEY"} {
		if strings.Contains(out, leak) {
			t.Errorf("output contains %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{`#EXT-X-MAP:URI="init.mp4"`, "\nseg0.mp4\n", "\nseg1.mp4\n", "#EXT-X-ENDLIST"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if up := r.Files["seg1.mp4"]; up.Path != "/videos/"+vid+"/hls1/main/1.mp4" || up.Query.Get("runtimeTicks") != "60000000" {
		t.Errorf("upstream for seg1: %+v", up)
	}
	if up := r.Files["init.mp4"]; up.Path != "/videos/"+vid+"/hls1/main/-1.mp4" {
		t.Errorf("upstream for init: %+v", up)
	}
}

func TestRewriteRefuses(t *testing.T) {
	bad := map[string]string{
		"absolute segment":    "#EXTM3U\nhttp://198.51.100.1/hls1/main/0.mp4\n",
		"protocol relative":   "#EXTM3U\n//198.51.100.1/hls1/main/0.mp4\n",
		"traversal":           "#EXTM3U\nhls1/../../System/0.mp4\n",
		"unknown segment":     "#EXTM3U\nhls1/main/0.exe\n",
		"rooted path":         "#EXTM3U\n/System/Info\n",
		"map elsewhere":       "#EXTM3U\n#EXT-X-MAP:URI=\"http://198.51.100.1/x.mp4\"\n",
		"not a playlist":      "<html>hello</html>",
		"segment in a subdir": "#EXTM3U\nhls1/main/deeper/0.mp4\n",
	}
	for name, body := range bad {
		if _, err := RewriteVariant([]byte(body), variantUp); !errors.Is(err, ErrBadPlaylist) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := RewriteMaster([]byte("#EXTM3U\n../evil.m3u8\n"), masterUp); !errors.Is(err, ErrBadPlaylist) {
		t.Errorf("master traversal: %v", err)
	}
}

func TestSanitizeVTT(t *testing.T) {
	in := "\uFEFFWEBVTT\r\n\r\nSTYLE\r\n::cue { background: url(http://198.51.100.1/x) }\r\n\r\nNOTE secret note\r\n\r\n" +
		"1\r\n00:00:01.000 --> 00:00:04.000 position:10% align:start\r\n<i>Hello</i> <script>alert(1)</script>\r\n\r\n" +
		"00:01:00.500 --> 00:01:02.000\r\n<c.yellow>World</c>\r\n"
	out, ok := SanitizeVTT([]byte(in))
	if !ok {
		t.Fatal("refused a valid file")
	}
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHello alert(1)\n\n00:01:00.500 --> 00:01:02.000\nWorld\n"
	if string(out) != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
	if _, ok := SanitizeVTT([]byte("<html>")); ok {
		t.Error("accepted a non-WebVTT file")
	}
	// Text that reads as markup in WebVTT is escaped, so it shows as is.
	out, _ = SanitizeVTT([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\nx < y & z > w, &lt;i&gt;\n"))
	if want := "WEBVTT\n\n00:01.000 --> 00:02.000\nx &lt; y &amp; z &gt; w,\n"; string(out) != want {
		t.Errorf("escaping: %q", out)
	}
}

// An HDR video Jellyfin copies comes with SDR versions it would re-encode,
// at the same bandwidth: only the copy is offered. Without a copy, every
// version stays.
func TestMasterKeepsTheCopy(t *testing.T) {
	hdr := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=9000000,VIDEO-RANGE=PQ,CODECS=\"hvc1.2.4.L153,mp4a.40.2\"\nmain.m3u8?VideoCodec=h264,hevc&ApiKey=SECRET\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=9000000,VIDEO-RANGE=SDR,CODECS=\"hvc1.1.4.L153,mp4a.40.2\"\nmain.m3u8?VideoCodec=hevc&AllowVideoStreamCopy=false&ApiKey=SECRET\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=9000000,VIDEO-RANGE=SDR,CODECS=\"avc1.640033,mp4a.40.2\"\nmain.m3u8?VideoCodec=h264&AllowVideoStreamCopy=false&ApiKey=SECRET\n"
	r, err := RewriteMaster([]byte(hdr), masterUp)
	if err != nil {
		t.Fatal(err)
	}
	if out := string(r.Body); len(r.Files) != 1 || !strings.Contains(out, "VIDEO-RANGE=PQ") || strings.Contains(out, "VIDEO-RANGE=SDR") {
		t.Errorf("copy only:\n%s", out)
	}
	sdrOnly := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=5000000\nmain.m3u8?VideoCodec=h264&AllowVideoStreamCopy=false&ApiKey=SECRET\n"
	if r, err := RewriteMaster([]byte(sdrOnly), masterUp); err != nil || len(r.Files) != 1 {
		t.Errorf("no copy: %v %v", r, err)
	}
}
