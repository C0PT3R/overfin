package gpu

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// Result is how one video transcoded.
type Result struct {
	Title string `json:"title"`
	Kind  string `json:"kind"` // "H.264", "HEVC 10-bit HDR"…
	OK    bool   `json:"ok"`
	// FirstPicture is how long the first bytes took, in seconds.
	FirstPicture float64 `json:"firstPicture,omitempty"`
	// Speed is how much faster than real time it went, once started.
	Speed float64 `json:"speed,omitempty"`
	// Reason is why it failed, from Jellyfin's transcoding log.
	Reason string `json:"reason,omitempty"`
}

// Problem is what's wrong, in plain words for people to read.
type Problem string

func (p Problem) Error() string { return string(p) }

// How long a video may take to start (a GPU setting up HDR tone mapping
// can take more than ten seconds), how long it's measured once started, and
// the bitrate asked for (the speed comes from how many seconds of video
// arrive).
var firstWait = 30 * time.Second // shorter in tests

const (
	testFor     = 8 * time.Second
	testBitrate = 4_000_000 + 128_000
	deviceID    = "overfin-gpu-test"
)

// Test sets Jellyfin's transcoding as settings say for a while, transcodes
// a few seconds of up to three videos from the libraries (H.264, HEVC
// 10-bit, preferably HDR, and AV1, those the libraries have), and puts the
// transcoding settings back as they were. progress, when set, hears how
// many videos there are, then each result as it comes.
func Test(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth, f Family, device string, progress func(total int, done []Result)) ([]Result, error) {
	if progress == nil {
		progress = func(int, []Result) {}
	}
	sources, err := jf.VideoSources(ctx, admin, 100)
	if err != nil {
		return nil, err
	}
	picked := pick(sources)
	if len(picked) == 0 {
		return nil, Problem("There's nothing to test with yet: add a library with films or series first.")
	}

	before, err := jf.Settings(ctx, admin, "encoding")
	if err != nil {
		return nil, err
	}
	trial := jellyfin.Settings{}
	for k, v := range before {
		trial[k] = v
	}
	for _, s := range f.Settings(device) {
		trial[s.Key] = s.Value
	}
	if err := jf.SetSettings(ctx, admin, "encoding", trial); err != nil {
		return nil, err
	}
	defer jf.SetSettings(context.WithoutCancel(ctx), admin, "encoding", before)

	a := jellyfin.Auth{Token: admin.Token, DeviceID: deviceID}
	var results []Result
	progress(len(picked), nil)
	for _, src := range picked {
		results = append(results, transcode(ctx, jf, a, src))
		progress(len(picked), slices.Clone(results))
	}
	return results, nil
}

// pick takes one H.264 video, one HEVC 10-bit (HDR if there's one) and one
// AV1, as far as the sample has them.
func pick(sources []jellyfin.VideoSource) []jellyfin.VideoSource {
	var out []jellyfin.VideoSource
	first := func(ok func(jellyfin.VideoSource) bool) {
		if i := slices.IndexFunc(sources, ok); i >= 0 {
			out = append(out, sources[i])
		}
	}
	first(func(s jellyfin.VideoSource) bool { return s.Codec == "h264" })
	if slices.ContainsFunc(sources, func(s jellyfin.VideoSource) bool { return s.Codec == "hevc" && s.BitDepth >= 10 && s.HDR }) {
		first(func(s jellyfin.VideoSource) bool { return s.Codec == "hevc" && s.BitDepth >= 10 && s.HDR })
	} else {
		first(func(s jellyfin.VideoSource) bool { return s.Codec == "hevc" })
	}
	first(func(s jellyfin.VideoSource) bool { return s.Codec == "av1" })
	return out
}

// kind names a video's format for people.
func kind(s jellyfin.VideoSource) string {
	k := map[string]string{"h264": "H.264", "hevc": "HEVC", "av1": "AV1"}[s.Codec]
	if s.BitDepth >= 10 {
		k += " 10-bit"
	}
	if s.HDR {
		k += " HDR"
	}
	return k
}

func transcode(ctx context.Context, jf *jellyfin.Client, a jellyfin.Auth, src jellyfin.VideoSource) Result {
	r := Result{Title: src.Name, Kind: kind(src)}
	session := strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(ctx, firstWait+testFor+5*time.Second)
	defer cancel()
	defer jf.StopEncoding(context.WithoutCancel(ctx), a, a.DeviceID, session)
	start := time.Now()
	res, err := jf.TestTranscode(ctx, a, src, session)
	if err != nil {
		r.Reason = why(context.WithoutCancel(ctx), jf, a, src, start, err)
		return r
	}
	defer res.Body.Close()
	// Without a first picture in time, the stream is closed: the read
	// below returns.
	var started atomic.Bool
	giveUp := time.AfterFunc(firstWait, func() {
		if !started.Load() {
			res.Body.Close()
		}
	})
	defer giveUp.Stop()
	var got int64
	var first time.Duration
	buf := make([]byte, 64<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 && !started.Load() {
			first = time.Since(start)
			started.Store(true)
		}
		got += int64(n)
		if err != nil || (started.Load() && time.Since(start)-first >= testFor) {
			break
		}
	}
	elapsed := time.Since(start)
	if got == 0 {
		r.Reason = why(context.WithoutCancel(ctx), jf, a, src, start, nil)
		return r
	}
	r.OK = true
	r.FirstPicture = round(first.Seconds())
	if running := (elapsed - first).Seconds(); running > 0 {
		r.Speed = round(float64(got) * 8 / testBitrate / running)
	}
	return r
}

func round(x float64) float64 { return float64(int(x*10+0.5)) / 10 }

// errorLine is a line of an ffmpeg log that says what went wrong.
var errorLine = regexp.MustCompile(`(?i)error|failed|not supported|no such|cannot|unable`)

// why reads the reason a transcode failed from Jellyfin's newest
// transcoding log for src, since start.
func why(ctx context.Context, jf *jellyfin.Client, a jellyfin.Auth, src jellyfin.VideoSource, start time.Time, failure error) string {
	plain := "Jellyfin refused to transcode it."
	switch {
	case failure == nil:
		plain = "No picture after " + firstWait.String() + "."
	case !jellyfin.IsStatus(failure, 500):
		plain = fmt.Sprintf("Jellyfin didn't transcode it (%v).", failure)
	}
	logs, err := jf.TranscodeLogs(ctx, a)
	if err != nil {
		return plain
	}
	var newest jellyfin.LogFile
	for _, l := range logs {
		mine := strings.Contains(l.Name, src.ItemID) || strings.Contains(l.Name, src.SourceID)
		if mine && l.Modified.After(start.Add(-2*time.Second)) && l.Modified.After(newest.Modified) {
			newest = l
		}
	}
	if newest.Name == "" {
		return plain
	}
	text, err := jf.TranscodeLog(ctx, a, newest.Name)
	if err != nil {
		return plain
	}
	var reasons []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(ffmpegPrefix.ReplaceAllString(line, ""))
		if errorLine.MatchString(line) && !slices.Contains(reasons, line) && len(line) < 300 {
			reasons = append(reasons, line)
		}
		if len(reasons) == 3 {
			break
		}
	}
	if len(reasons) == 0 {
		return plain
	}
	return plain + " " + strings.Join(reasons, " · ")
}

// ffmpegPrefix is ffmpeg's "[name @ 0x1234]" before a message.
var ffmpegPrefix = regexp.MustCompile(`\[[^\]]* @ 0x[0-9a-f]+\]\s*`)
