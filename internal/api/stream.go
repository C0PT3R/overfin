package api

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
)

// Stream serves what a playback session plays: the media file, rewritten
// HLS playlists, segments and subtitles. Every response is looked up through
// the stream ticket; the real Jellyfin URLs, which carry its token, never
// leave the server (SECURITY.md rules 4, 8, 11 and 12).
type Stream struct {
	Playback Playback
}

func (Stream) Name() string { return "stream" }

func (s Stream) Register(r *plugin.Registrar) error {
	if err := r.HandleFunc("GET /api/v1/stream/{ticket}/{name}", s.serve); err != nil {
		return err
	}
	return r.HandleFunc("GET /api/v1/stream/{ticket}/subtitles/{track}", s.subtitles)
}

const (
	maxPlaylist  = 2 << 20
	maxSubtitles = 5 << 20
)

var (
	ticketRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	fileNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	rangeRe    = regexp.MustCompile(`^bytes=\d{0,15}-\d{0,15}$`)
)

// stream finds the playback behind a ticket, for its own session only.
func (s Stream) stream(w http.ResponseWriter, r *http.Request) (*playback.Stream, *jellyfin.Auth) {
	ticket := r.PathValue("ticket")
	if !ticketRe.MatchString(ticket) {
		problem.Send(w, problem.NotFound, "")
		return nil, nil
	}
	l := s.Playback.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return nil, nil
	}
	p := c.Profile
	// The ticket works only in the session that started the playback
	// (rule 30): a copied stream URL is useless anywhere else.
	st, ok := s.Playback.Tickets.ByTicket(ticket, time.Now())
	if !ok || st.SessionID != c.Session.ID || st.ProfileID != p.ID {
		problem.Send(w, problem.NotFound, "")
		return nil, nil
	}
	// As the playback's own device, so its transcode is the one served.
	a := st.Auth
	return st, &a
}

func (s Stream) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if len(r.URL.Query()) > 0 || !fileNameRe.MatchString(name) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, a := s.stream(w, r)
	if st == nil {
		return
	}
	dec := st.Decision
	switch {
	case name == "file" && dec.Protocol == "file":
		s.proxy(w, r, *a, dec.Upstream, true, "")
	case name == "master.m3u8" && dec.Protocol == "hls":
		s.playlist(w, r, st, *a, dec.Upstream, playback.RewriteMaster)
	case strings.HasSuffix(name, ".m3u8"):
		up, ok := st.File(name)
		if !ok {
			problem.Send(w, problem.NotFound, "")
			return
		}
		s.playlist(w, r, st, *a, up, playback.RewriteVariant)
	default:
		up, ok := st.File(name)
		if !ok {
			problem.Send(w, problem.NotFound, "")
			return
		}
		contentType := "video/mp4"
		switch {
		case strings.HasSuffix(name, ".ts"):
			contentType = "video/mp2t"
		case strings.HasSuffix(name, ".aac"):
			contentType = "audio/aac"
		case strings.HasSuffix(name, ".mp3"):
			contentType = "audio/mpeg"
		}
		s.proxy(w, r, *a, up, false, contentType)
	}
}

// playlist fetches an upstream playlist, rewrites it and remembers the real
// URLs behind its new names.
func (s Stream) playlist(w http.ResponseWriter, r *http.Request, st *playback.Stream, a jellyfin.Auth,
	up playback.Upstream, rewrite func([]byte, playback.Upstream) (*playback.Rewritten, error)) {

	l := s.Playback.Library
	body, err := l.Jellyfin.ReadSmall(r.Context(), a, up.Path, up.Query, maxPlaylist)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	rw, err := rewrite(body, up)
	if err != nil {
		l.Logger.Warn("refused an upstream playlist", "playback", st.ID, "err", err)
		problem.Send(w, problem.NotPlayable, "")
		return
	}
	st.AddFiles(rw.Files)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(rw.Body)))
	_, _ = w.Write(rw.Body)
}

// proxy streams bytes from upstream without buffering. Only the headers
// streaming needs are passed on (rule 12); for direct play the client's
// Range header is forwarded so seeking works.
func (s Stream) proxy(w http.ResponseWriter, r *http.Request, a jellyfin.Auth, up playback.Upstream,
	allowRange bool, forceType string) {

	rangeHeader := ""
	if allowRange {
		if rh := r.Header.Get("Range"); rh != "" {
			if !rangeRe.MatchString(rh) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			rangeHeader = rh
		}
	}
	l := s.Playback.Library
	res, err := l.Jellyfin.Open(r.Context(), a, up.Path, up.Query, rangeHeader)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	defer res.Body.Close()

	h := w.Header()
	contentType := forceType
	if contentType == "" {
		contentType = mediaType(res.Header.Get("Content-Type"))
	}
	h.Set("Content-Type", contentType)
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := res.Header.Get(k); v != "" && len(v) < 200 {
			h.Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	// io.Copy streams in small chunks: memory stays flat however long the
	// film. If the client goes away, the request context ends the upstream
	// read too.
	_, _ = io.Copy(w, res.Body)
}

// mediaType keeps an upstream content type only when it is audio or video;
// anything else is sent as opaque bytes, never as something a browser would
// render (and nosniff is set on every response).
func mediaType(ct string) string {
	t, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(ct)), ";")
	if (strings.HasPrefix(t, "video/") || strings.HasPrefix(t, "audio/")) && fileNameRe.MatchString(strings.ReplaceAll(t, "/", "-")) {
		return t
	}
	return "application/octet-stream"
}

// subtitles serves one text subtitle track as sanitized WebVTT.
func (s Stream) subtitles(w http.ResponseWriter, r *http.Request) {
	track := r.PathValue("track")
	if len(r.URL.Query()) > 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	index, ok := trackIndex(track, 's')
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, a := s.stream(w, r)
	if st == nil {
		return
	}
	if !st.TextSubtitle(index) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	l := s.Playback.Library
	path := "/Videos/" + st.ItemID + "/" + st.SourceID + "/Subtitles/" + strconv.Itoa(index) + "/Stream.vtt"
	body, err := l.Jellyfin.ReadSmall(r.Context(), *a, path, nil, maxSubtitles)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	vtt, ok := playback.SanitizeVTT(body)
	if !ok {
		l.Logger.Warn("refused upstream subtitles", "playback", st.ID)
		problem.Send(w, problem.NotFound, "")
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(vtt)))
	_, _ = w.Write(vtt)
}
