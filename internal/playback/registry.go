package playback

import (
	"crypto/rand"
	"encoding/base64"
	"sort"
	"sync"
	"time"

	"github.com/C0PT3R/overfin/internal/ids"
	"github.com/C0PT3R/overfin/internal/jellyfin"
)

// Stream is one active playback, as Overfin keeps it. Playback state is
// short-lived and only kept in memory (rule 40).
type Stream struct {
	ID            string // play_…, the playback's public ID
	Ticket        string // secret, in stream URLs only
	AccountID     string
	ProfileID     string
	SessionID     string // the browser session that started it (rule 30)
	Device        string // a readable name, shown when another device takes over
	ItemID        string // Jellyfin IDs
	SourceID      string
	PlaySessionID string
	Decision      Decision
	Auth          jellyfin.Auth // to report to Jellyfin after the request is gone
	AudioIndex    *int
	SubtitleIndex *int
	View          any // the session as last sent to the client, for restoring

	// Guarded by the registry's lock.
	started       time.Time
	lastSeen      time.Time
	positionTicks int64
	paused        bool

	mu sync.Mutex
	// files maps the neutral names in rewritten playlists to the real
	// upstream URLs. It grows as playlists are fetched.
	files map[string]Upstream
}

// AddFiles records the real URLs behind a rewritten playlist's names.
func (s *Stream) AddFiles(files map[string]Upstream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string]Upstream{}
	}
	for k, v := range files {
		s.files[k] = v
	}
}

// File returns the real URL behind a neutral name.
func (s *Stream) File(name string) (Upstream, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.files[name]
	return u, ok
}

// TextSubtitle reports whether index is a text subtitle track of the
// stream's version, the only kind served as WebVTT.
func (s *Stream) TextSubtitle(index int) bool {
	for _, ms := range s.Decision.Source.MediaStreams {
		if ms.Type == "Subtitle" && ms.Index == index && ms.IsTextSubtitleStream {
			return true
		}
	}
	return false
}

// PlayMethod is the stream's method in Jellyfin's terms.
func (s *Stream) PlayMethod() string {
	switch s.Decision.Method {
	case "direct":
		return "DirectPlay"
	case "remux":
		return "DirectStream"
	}
	return "Transcode"
}

// Ended remembers why a playback stopped, so its device can be told.
type Ended struct {
	Reason string // "taken-over", "expired" or "stopped"
	By     string // for "taken-over": the device that took over
	at     time.Time
}

// Snapshot is a stream's activity at one moment.
type Snapshot struct {
	PositionTicks int64
	Paused        bool
}

// Tickets is the registry of playbacks.
type Tickets struct {
	mu       sync.Mutex
	byTicket map[string]*Stream
	byID     map[string]*Stream
	ended    map[string]Ended
	// MaxAge is a hard limit on any playback, however active.
	MaxAge time.Duration
}

// endedFor is how long the reason a playback ended is remembered.
const endedFor = time.Hour

// NewTickets returns an empty registry.
func NewTickets(maxAge time.Duration) *Tickets {
	return &Tickets{byTicket: map[string]*Stream{}, byID: map[string]*Stream{}, ended: map[string]Ended{}, MaxAge: maxAge}
}

// Add registers a stream and gives it its public ID and secret ticket.
func (t *Tickets) Add(s *Stream, startTicks int64, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.ID = ids.New("play")
	s.Ticket = random(24)
	s.started, s.lastSeen, s.positionTicks = now, now, startTicks
	t.byTicket[s.Ticket] = s
	t.byID[s.ID] = s
}

func (t *Tickets) live(s *Stream, now time.Time) bool {
	return now.Sub(s.started) < t.MaxAge
}

// ByTicket finds a live stream by its ticket. A request for the stream
// counts as activity.
func (t *Tickets) ByTicket(ticket string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byTicket[ticket]
	if !ok || !t.live(s, now) {
		return nil, false
	}
	s.lastSeen = now
	return s, true
}

// ByID finds a playback by its public ID, only for the profile that owns it
// (rule 23). For a playback that ended, it returns why instead.
func (t *Tickets) ByID(id, profileID string, now time.Time) (*Stream, *Ended, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.byID[id]; ok && s.ProfileID == profileID && t.live(s, now) {
		return s, nil, true
	}
	if e, ok := t.ended[id+"/"+profileID]; ok && now.Sub(e.at) < endedFor {
		return nil, &e, true
	}
	return nil, nil, false
}

// Progress records a progress report.
func (t *Tickets) Progress(s *Stream, positionTicks int64, paused bool, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.positionTicks, s.paused, s.lastSeen = positionTicks, paused, now
}

// Snapshot returns a stream's latest activity.
func (t *Tickets) Snapshot(s *Stream) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Snapshot{PositionTicks: s.positionTicks, Paused: s.paused}
}

// Remove ends a playback and remembers why. It returns false if the
// playback had already ended, so cleanup runs only once.
func (t *Tickets) Remove(s *Stream, reason, by string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID[s.ID] != s {
		return false
	}
	delete(t.byID, s.ID)
	delete(t.byTicket, s.Ticket)
	t.ended[s.ID+"/"+s.ProfileID] = Ended{Reason: reason, By: by, at: now}
	for k, e := range t.ended {
		if now.Sub(e.at) >= endedFor {
			delete(t.ended, k)
		}
	}
	return true
}

// ForAccount lists an account's playbacks, oldest first.
func (t *Tickets) ForAccount(accountID string) []*Stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*Stream
	for _, s := range t.byID {
		if s.AccountID == accountID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].started.Before(out[j].started) })
	return out
}

// Transcodes counts playbacks that make Jellyfin transcode or repackage.
func (t *Tickets) Transcodes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, s := range t.byID {
		if s.Decision.Protocol == "hls" {
			n++
		}
	}
	return n
}

// Latest returns the most recently started playback of a session by one
// of its profiles.
func (t *Tickets) Latest(sessionID, profileID string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best *Stream
	for _, s := range t.byID {
		if s.SessionID == sessionID && s.ProfileID == profileID && t.live(s, now) &&
			(best == nil || s.started.After(best.started)) {
			best = s
		}
	}
	return best, best != nil
}

// Idle lists playbacks with no activity for longer than idle, and those past
// their maximum age.
func (t *Tickets) Idle(now time.Time, idle time.Duration) []*Stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*Stream
	for _, s := range t.byID {
		if now.Sub(s.lastSeen) > idle || !t.live(s, now) {
			out = append(out, s)
		}
	}
	return out
}

// random returns n random bytes as URL-safe text. Tickets get 24 bytes, well
// above the 128-bit minimum (rule 23).
func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
