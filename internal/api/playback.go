package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/textclean"
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
		"POST /api/v1/playback":                       pb.start,
		"GET /api/v1/playback/current":                pb.current,
		"GET /api/v1/playback/{id}":                   pb.get,
		"POST /api/v1/playback/{id}/progress":         pb.progress,
		"POST /api/v1/playback/{id}/stop":             pb.stop,
		"GET /api/v1/playback/{id}/trickplay/{sheet}": pb.trickplaySheet,
		"PUT /api/v1/me/subtitles":                    pb.setSubtitles,
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
	// TitleID is what to play (milestone 5): Overfin picks a copy the
	// profile may see. ItemID, a copy, is the older way.
	TitleID         string                `json:"titleId"`
	ItemID          string                `json:"itemId"`
	Capabilities    playback.Capabilities `json:"capabilities"`
	StartPositionMs int64                 `json:"startPositionMs"`
	VersionID       string                `json:"versionId"`
	AudioTrack      string                `json:"audioTrack"`
	SubtitleTrack   string                `json:"subtitleTrack"`
	MaxBitrateKbps  int                   `json:"maxBitrateKbps"`
	// AudioLanguage is the language to start in (the site's), when the
	// video has it and no AudioTrack is asked for.
	AudioLanguage string   `json:"audioLanguage"`
	Queue         []string `json:"queue"`
	Device        string   `json:"device"`
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

// trickplayJSON is a set of seek-bar previews (the API's Trickplay).
type trickplayJSON struct {
	TileWidth  int      `json:"tileWidth"`
	TileHeight int      `json:"tileHeight"`
	Columns    int      `json:"columns"`
	Rows       int      `json:"rows"`
	IntervalMs int      `json:"intervalMs"`
	TileCount  int      `json:"tileCount"`
	Sheets     []string `json:"sheets"`
}

type streamJSON struct {
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	MimeType string `json:"mimeType,omitempty"`
}

type sessionJSON struct {
	ID string `json:"id"`
	// Title is what plays, as titles are shown everywhere else.
	Title          *library.TitleSummary `json:"title,omitempty"`
	Method         string                `json:"method"`
	Stream         streamJSON            `json:"stream"`
	PositionMs     int64                 `json:"positionMs"`
	DurationMs     int64                 `json:"durationMs,omitempty"`
	Versions       []versionJSON         `json:"versions,omitempty"`
	VersionID      string                `json:"versionId,omitempty"`
	AudioTracks    []trackJSON           `json:"audioTracks,omitempty"`
	AudioTrack     string                `json:"audioTrack,omitempty"`
	SubtitleTracks []trackJSON           `json:"subtitleTracks,omitempty"`
	SubtitleTrack  string                `json:"subtitleTrack,omitempty"`
	MaxBitrateKbps int                   `json:"maxBitrateKbps,omitempty"`
	Segments       []segmentJSON         `json:"segments,omitempty"`
	Trickplay      *trickplayJSON        `json:"trickplay,omitempty"`
	// Next is the episode after this one, when it's one the profile may
	// see: the rest of the season, then the next season's.
	Next *library.TitleSummary `json:"next,omitempty"`
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
	if req.TitleID != "" {
		_, ok = titleRef(req.TitleID)
		ok = ok && req.ItemID == ""
	}
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
	if req.TitleID != "" {
		var err error
		if jfItem, err = l.copyOf(ctx, p, req.TitleID); err != nil {
			l.storeError(w, err)
			return
		}
		if jfItem == "" {
			problem.Send(w, problem.NotFound, "")
			return
		}
	}

	// Overfin's one Jellyfin account plays, as a device of this playback's
	// own: Jellyfin keeps one session per device, and ties each transcode
	// to the device that asked for it.
	ja, uid, ok := l.account(w, r)
	if !ok {
		return
	}
	play := ja
	play.DeviceID = pb.Tickets.ReserveDevice("overfin-play")
	started := false
	defer func() {
		if !started {
			pb.Tickets.ReleaseDevice(play.DeviceID)
		}
	}()

	raw, err := l.Jellyfin.Item(ctx, ja, uid, jfItem)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	if may, err := l.mayOpen(ctx, p, raw); err != nil || !may {
		l.hidden(w, err)
		return
	}
	summary, ok := library.Summary(raw)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	// Without a subtitle track asked for, none is burned in: the profile's
	// language, if any, is chosen below.
	if req.SubtitleTrack == "" {
		off := -1
		pr.SubtitleStreamIndex = &off
	}
	audioItem := summary.Kind == "track" || summary.Kind == "audiobook"
	var info *jellyfin.PlaybackInfo
	var dec *playback.Decision
	decide := func() bool {
		var err error
		if info, err = l.Jellyfin.PlaybackInfo(ctx, play, uid, jfItem, pr); err != nil {
			l.upstreamError(w, err)
			return false
		}
		dec, err = playback.Decide(info, jfItem, audioItem, play.DeviceID)
		switch {
		case errors.Is(err, playback.ErrNotPlayable):
			problem.Send(w, problem.NotPlayable, "")
			return false
		case errors.Is(err, playback.ErrUnsafeUpstream):
			pb.Logger.Warn("refused an unexpected stream URL from Jellyfin", "item", req.ItemID, "err", err)
			problem.Send(w, problem.NotPlayable, "")
			return false
		case err != nil:
			l.upstreamError(w, err)
			return false
		}
		return true
	}
	if !decide() {
		return
	}
	// The languages: the audio in the one asked for (the site's), the
	// subtitles in the profile's, when the video has them and no track was
	// asked for. Jellyfin is asked again only when that changes the stream
	// (another audio track, subtitles burned into the picture).
	textSubtitle := ""
	again := false
	if req.AudioTrack == "" && req.AudioLanguage != "" {
		if n, ok := audioIn(dec.Source, req.AudioLanguage); ok && n != defaultAudio(dec.Source) {
			pr.AudioStreamIndex, again = &n, true
		}
	}
	if req.SubtitleTrack == "" && p.SubtitleLanguage != "" {
		if n, text, ok := subtitlesIn(dec.Source, p.SubtitleLanguage); ok && text {
			textSubtitle = "s" + strconv.Itoa(n)
		} else if ok {
			pr.SubtitleStreamIndex, again = &n, true
		}
	}
	if again && !decide() {
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
		Auth: play, AudioIndex: pr.AudioStreamIndex, SubtitleIndex: pr.SubtitleStreamIndex,
	}
	pb.Tickets.Add(s, pr.StartTimeTicks, time.Now())
	started = true // the device is the playback's now, freed when it ends

	titles, err := l.titlesOfItems(ctx, p, []string{jfItem})
	if err != nil {
		l.storeError(w, err)
		return
	}
	out := sessionJSON{
		ID: s.ID, Method: dec.Method,
		PositionMs: req.StartPositionMs, DurationMs: dec.Source.RunTimeTicks / 10_000,
		VersionID: "ver_" + sourceID, MaxBitrateKbps: kbps,
	}
	if len(titles) == 1 {
		out.Title = &titles[0]
		if ref, ok := titleRef(titles[0].ID); ok && ref.Kind == "episode" {
			next, err := l.nextEpisode(ctx, p, ref)
			if err != nil {
				pb.Logger.Warn("finding the next episode failed", "err", err) // a bonus: playback goes on
			}
			out.Next = next
		}
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
		out.AudioTrack = "a" + strconv.Itoa(*pr.AudioStreamIndex)
	} else if i := defaultAudio(dec.Source); i >= 0 {
		out.AudioTrack = "a" + strconv.Itoa(i)
	}
	switch {
	case textSubtitle != "":
		out.SubtitleTrack = textSubtitle // shown by the client, from its URL
	case pr.SubtitleStreamIndex != nil && *pr.SubtitleStreamIndex >= 0:
		out.SubtitleTrack = "s" + strconv.Itoa(*pr.SubtitleStreamIndex)
	}
	// Skip markers are a bonus: playback goes on without them.
	if segs, err := l.Jellyfin.MediaSegments(ctx, ja, jfItem); err == nil {
		for _, sg := range segs {
			if kind := strings.ToLower(sg.Type); kind != "unknown" && kind != "" && sg.EndTicks > sg.StartTicks {
				out.Segments = append(out.Segments, segmentJSON{Kind: kind, StartMs: sg.StartTicks / 10_000, EndMs: sg.EndTicks / 10_000})
			}
		}
	}

	// Seek-bar previews are a bonus too: the set nearest 320 pixels wide.
	if sets, err := l.Jellyfin.Trickplays(ctx, ja, uid, jfItem, sourceID); err == nil && len(sets) > 0 {
		best := sets[0]
		for _, t := range sets[1:] {
			if max(t.Width-320, 320-t.Width) < max(best.Width-320, 320-best.Width) {
				best = t
			}
		}
		s.Trickplay = &best
		tp := &trickplayJSON{TileWidth: best.Width, TileHeight: best.Height, Columns: best.Columns, Rows: best.Rows,
			IntervalMs: best.IntervalMs, TileCount: best.Count}
		perSheet := best.Columns * best.Rows
		for n := 0; n*perSheet < best.Count; n++ {
			tp.Sheets = append(tp.Sheets, "/api/v1/playback/"+s.ID+"/trickplay/"+strconv.Itoa(n))
		}
		out.Trickplay = tp
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

// trickplaySheet serves one sheet of a playback's seek-bar previews, to
// whoever may see the playback, re-encoded like every image (rule 10).
func (pb Playback) trickplaySheet(w http.ResponseWriter, r *http.Request) {
	s := pb.find(w, r)
	if s == nil {
		return
	}
	n, err := strconv.Atoi(r.PathValue("sheet"))
	tp := s.Trickplay
	if err != nil || tp == nil || n < 0 || n*tp.Columns*tp.Rows >= tp.Count || strconv.Itoa(n) != r.PathValue("sheet") {
		problem.Send(w, problem.NotFound, "")
		return
	}
	l := pb.Library
	key := fmt.Sprintf("trickplay/%s/%s/%d/%d", s.ItemID, s.SourceID, tp.Width, n)
	data, err := l.Images.Fetched(r.Context(), key, func(ctx context.Context) ([]byte, error) {
		return l.Jellyfin.TrickplaySheet(ctx, s.Auth, s.ItemID, s.SourceID, tp.Width, n)
	})
	if err != nil {
		if r.Context().Err() == nil {
			pb.Logger.Warn("a seek-bar preview sheet failed", "playback", s.ID, "err", err)
		}
		problem.Send(w, problem.NotFound, "")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// defaultAudio is the index of the audio track a version plays unless
// asked otherwise: Jellyfin's default, else the one marked default, else
// the first; -1 when it has none.
func defaultAudio(src jellyfin.MediaSource) int {
	if i := src.DefaultAudioStreamIndex; i != nil {
		return *i
	}
	first := -1
	for _, ms := range src.MediaStreams {
		if ms.Type != "Audio" {
			continue
		}
		if ms.IsDefault {
			return ms.Index
		}
		if first < 0 {
			first = ms.Index
		}
	}
	return first
}

// audioIn is the index of a version's audio track in a language: its
// default one if that's in it, else the first.
func audioIn(src jellyfin.MediaSource, language string) (int, bool) {
	found, ok := 0, false
	for _, ms := range src.MediaStreams {
		if ms.Type != "Audio" || !lang.Same(ms.Language, language) {
			continue
		}
		if ms.IsDefault {
			return ms.Index, true
		}
		if !ok {
			found, ok = ms.Index, true
		}
	}
	return found, ok
}

// subtitlesIn is the index of a version's subtitle track in a language,
// and whether it's text (shown by the client) rather than pictures
// (burned in): text first, whole subtitles before forced ones and those
// for the hearing impaired.
func subtitlesIn(src jellyfin.MediaSource, language string) (index int, text, ok bool) {
	best := -1
	for _, ms := range src.MediaStreams {
		if ms.Type != "Subtitle" || !lang.Same(ms.Language, language) {
			continue
		}
		score := 0
		if ms.IsTextSubtitleStream {
			score += 4
		}
		if !ms.IsForced {
			score += 2
		}
		if !ms.IsHearingImpaired {
			score++
		}
		if score > best {
			best, index, text = score, ms.Index, ms.IsTextSubtitleStream
		}
	}
	return index, text, best >= 0
}

// setSubtitles keeps the language the active profile chose for subtitles
// ("" for none): its next playbacks start with them, when the video has
// them.
func (pb Playback) setSubtitles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language string `json:"language"`
	}
	if !decode(r, &req) || (req.Language != "" && lang.Base(req.Language) == "") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	l := pb.Library
	p := l.active(w, r)
	if p == nil {
		return
	}
	if err := l.Provisioner.Store.SetSubtitleLanguage(r.Context(), p.AccountID, p.ID, lang.Base(req.Language)); err != nil {
		l.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
