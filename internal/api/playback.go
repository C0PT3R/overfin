package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/library"
	"github.com/C0PT3R/overfin-server/internal/playback"
	"github.com/C0PT3R/overfin-server/internal/plugin"
	"github.com/C0PT3R/overfin-server/internal/problem"
	"github.com/C0PT3R/overfin-server/internal/textclean"
)

// Playback starts, follows, ends and restores playbacks.
type Playback struct {
	Library        Library // for the active profile and Jellyfin access
	Tickets        *playback.Tickets
	MaxBitrateKbps int // the operator's cap; 0 means none
	// MaxTranscodes in total; 0 means no limit. Streams per account come
	// from the account's template.
	MaxTranscodes int
	// RefuseOverLimit refuses a stream over the account's limit instead of
	// replacing its oldest.
	RefuseOverLimit bool
	// VersionNames shows Jellyfin's version names, derived from file names,
	// instead of neutral descriptions (privacy.expose "version.names").
	VersionNames bool
	Logger       *slog.Logger
}

func (Playback) Name() string { return "playback" }

func (pb Playback) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"POST /api/v1/playback":               pb.start,
		"GET /api/v1/playback/current":        pb.current,
		"GET /api/v1/playback/{id}":           pb.get,
		"POST /api/v1/playback/{id}/progress": pb.progress,
		"POST /api/v1/playback/{id}/stop":     pb.stop,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// IdleTimeout ends a playback with neither progress reports nor stream
// requests for this long. Clients report every few seconds, paused or not.
const IdleTimeout = 2 * time.Minute

// RunJanitor ends idle playbacks, and their transcodes, until ctx is done.
// Jellyfin doesn't stop abandoned transcodes by itself (rule 33).
func (pb Playback) RunJanitor(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			for _, s := range pb.Tickets.Idle(now, IdleTimeout) {
				pb.end(s, "expired", "")
			}
		}
	}
}

// end stops a playback once: it is forgotten at once, and Jellyfin is told
// in the background, its transcode included.
func (pb Playback) end(s *playback.Stream, reason, by string) {
	if !pb.Tickets.Remove(s, reason, by, time.Now()) {
		return
	}
	snap := pb.Tickets.Snapshot(s)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Where the profile is in the title, now that the playback is over.
		pb.record(ctx, s, snap.PositionTicks/10_000, true)
		jf := pb.Library.Jellyfin
		if err := jf.ReportStopped(ctx, s.Auth, pb.state(s, snap)); err != nil {
			pb.Logger.Warn("could not report a stopped playback", "playback", s.ID, "err", err)
		}
		if s.Decision.Protocol == "hls" {
			if err := jf.StopEncoding(ctx, s.Auth, s.Auth.DeviceID, s.PlaySessionID); err != nil {
				pb.Logger.Warn("could not stop a transcode", "playback", s.ID, "err", err)
			}
		}
	}()
}

func (pb Playback) state(s *playback.Stream, snap playback.Snapshot) jellyfin.PlayState {
	return jellyfin.PlayState{
		ItemId: s.ItemID, MediaSourceId: s.SourceID, PlaySessionId: s.PlaySessionID,
		PositionTicks: snap.PositionTicks, IsPaused: snap.Paused, CanSeek: true,
		PlayMethod: s.PlayMethod(), AudioStreamIndex: s.AudioIndex, SubtitleStreamIndex: s.SubtitleIndex,
	}
}

// ---- Progress, stop, restore ---------------------------------------------------

type progressRequest struct {
	PositionMs int64 `json:"positionMs"`
	Paused     bool  `json:"paused"`
	Ended      bool  `json:"ended"`
}

var playIDRe = regexp.MustCompile(`^play_[a-z2-7]{26}$`)

// find returns the caller's playback, or writes the right answer: 404, or
// 410 with the reason it ended.
func (pb Playback) find(w http.ResponseWriter, r *http.Request) *playback.Stream {
	id := r.PathValue("id")
	if !playIDRe.MatchString(id) {
		problem.Send(w, problem.NotFound, "")
		return nil
	}
	p := pb.Library.active(w, r)
	if p == nil {
		return nil
	}
	s, ended, ok := pb.Tickets.ByID(id, p.ID, time.Now())
	switch {
	case !ok:
		problem.Send(w, problem.NotFound, "")
	case ended != nil:
		sendEnded(w, ended)
	default:
		return s
	}
	return nil
}

func sendEnded(w http.ResponseWriter, e *playback.Ended) {
	switch e.Reason {
	case "taken-over":
		prob := problem.New(problem.PlaybackTakenOver, "")
		prob.TakenOverBy = e.By
		problem.Write(w, prob)
	case "expired":
		problem.Send(w, problem.PlaybackExpired, "")
	default:
		problem.Send(w, problem.PlaybackStopped, "")
	}
}

func (pb Playback) progress(w http.ResponseWriter, r *http.Request) {
	var req progressRequest
	if !decode(r, &req) || req.PositionMs < 0 {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	s := pb.find(w, r)
	if s == nil {
		return
	}
	pb.Tickets.Progress(s, req.PositionMs*10_000, req.Paused, time.Now())
	pb.record(r.Context(), s, req.PositionMs, false)
	if pb.Tickets.Method(s) == "remux" {
		s.Checked.Do(func() { go pb.checkRemux(s) })
	}
	if err := pb.Library.Jellyfin.ReportProgress(r.Context(), s.Auth, pb.state(s, pb.Tickets.Snapshot(s))); err != nil {
		pb.Logger.Warn("could not report progress", "playback", s.ID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// stop is safe to call more than once: a playback already ended, or unknown,
// is simply already stopped.
func (pb Playback) stop(w http.ResponseWriter, r *http.Request) {
	var req progressRequest
	if !decode(r, &req) || req.PositionMs < 0 || !playIDRe.MatchString(r.PathValue("id")) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := pb.Library.active(w, r)
	if p == nil {
		return
	}
	if s, ended, ok := pb.Tickets.ByID(r.PathValue("id"), p.ID, time.Now()); ok && ended == nil {
		position := req.PositionMs
		if req.Ended { // played to the end, whatever the last position said
			position = max(position, s.Decision.Source.RunTimeTicks/10_000)
		}
		pb.Tickets.Progress(s, position*10_000, false, time.Now())
		pb.end(s, "stopped", "")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (pb Playback) get(w http.ResponseWriter, r *http.Request) {
	if s := pb.find(w, r); s != nil {
		pb.writeView(w, s)
	}
}

// current restores this session's latest playback by the active profile, at
// its last reported position, after a reload. Another profile's playback on
// the same device stays private (rule 50).
func (pb Playback) current(w http.ResponseWriter, r *http.Request) {
	c := pb.Library.Sessions.Current(w, r)
	if c == nil {
		return
	}
	s, ok := pb.Tickets.Latest(c.Session.ID, c.Profile.ID, time.Now())
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	pb.writeView(w, s)
}

func (pb Playback) writeView(w http.ResponseWriter, s *playback.Stream) {
	view, ok := s.View.(sessionJSON)
	if !ok {
		problem.Send(w, problem.Internal, "")
		return
	}
	view.PositionMs = pb.Tickets.Snapshot(s).PositionTicks / 10_000
	writeOK(w, view)
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
	caller := l.Sessions.Current(w, r)
	if caller == nil {
		return
	}
	p := caller.Profile
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
		pb.Logger.Warn("refused an unexpected stream URL from Jellyfin", "item", req.ItemID, "err", err)
		problem.Send(w, problem.NotPlayable, "")
		return
	case err != nil:
		l.upstreamError(w, err)
		return
	}

	// The account's stream limit: replace its oldest playback, whose device
	// is then told who took over, or refuse (rule 32). Another account's
	// playback is never touched.
	device := req.Device
	if device == "" {
		device = "another device"
	}
	limit := caller.Account.Template.MaxStreams
	if limit > 0 {
		active := pb.Tickets.ForAccount(p.AccountID)
		if len(active) >= limit {
			if pb.RefuseOverLimit {
				problem.Send(w, problem.StreamLimitReached, "")
				return
			}
			for _, old := range active[:len(active)-limit+1] {
				pb.end(old, "taken-over", device)
			}
		}
	}
	if dec.Method == "transcode" && pb.MaxTranscodes > 0 && pb.Tickets.Transcodes() >= pb.MaxTranscodes {
		problem.Send(w, problem.ServerBusy, "")
		return
	}

	sourceID, _ := jellyfin.NormalizeID(dec.Source.Id)
	s := &playback.Stream{
		AccountID: p.AccountID, ProfileID: p.ID, SessionID: caller.Session.ID, Device: device,
		ItemID: jfItem, SourceID: sourceID, PlaySessionID: info.PlaySessionId, Decision: *dec,
		Auth: auth(p), AudioIndex: pr.AudioStreamIndex, SubtitleIndex: pr.SubtitleStreamIndex,
	}
	pb.Tickets.Add(s, pr.StartTimeTicks, time.Now())

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
			name := playback.Describe(src)
			if pb.VersionNames {
				name = textclean.Plain(src.Name, 200)
			}
			out.Versions = append(out.Versions, versionJSON{ID: "ver_" + id, Name: name})
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

	s.View = out
	if err := l.Jellyfin.ReportStart(ctx, s.Auth, pb.state(s, pb.Tickets.Snapshot(s))); err != nil {
		pb.Logger.Warn("could not report a playback start", "playback", s.ID, "err", err)
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

// checkRemux asks Jellyfin, once the stream runs, whether a remux really
// copies the video. The reasons Jellyfin gives beforehand don't cover
// everything it decides when it starts encoding (converting HDR for a
// screen, say); a remux that re-encodes is a transcode, and counts as one
// from then on.
func (pb Playback) checkRemux(s *playback.Stream) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	copied, found, err := pb.Library.Jellyfin.VideoCopied(ctx, s.Auth, s.Auth.DeviceID, s.ItemID)
	if err != nil {
		pb.Logger.Warn("could not check how Jellyfin serves a playback", "playback", s.ID, "err", err)
		return
	}
	if found && !copied {
		pb.Tickets.Reencodes(s)
		pb.Logger.Info("a playback taken for a remux re-encodes its video: counted as a transcode", "playback", s.ID)
	}
}
