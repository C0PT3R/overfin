package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"

	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/password"
)

// Setup is the first-run setup page, served until Overfin is set up: on
// the control panel's address, and with managed sign-in on the site's own
// too. It asks for what's missing: Overfin's own Jellyfin (SECURITY.md
// rule 67), which it sets up if it's fresh, and otherwise shows, then takes
// only when its administrator hands it over; and with managed sign-in the
// first admin's email and password. Only someone with the one-time code
// from Overfin's log can use it, so nobody else can claim a fresh install.
type Setup struct {
	// Code is the one-time code Overfin printed in its log (NewSetupCode).
	Code string
	// Jellyfin takes one step towards Overfin's Jellyfin, and saves it once
	// it's Overfin's (Done). Its error says what's wrong in plain words, and
	// is shown as is. Nil when Jellyfin is set up already.
	Jellyfin func(ctx context.Context, req JellyfinRequest) (JellyfinAnswer, error)
	// FixedURL is Jellyfin's address when the configuration file sets it:
	// the page doesn't ask for it then.
	FixedURL string
	// Language, when set, also asks for the default language (a
	// lang.Choices tag), and saves it.
	Language func(ctx context.Context, tag string) error
	// FirstAdmin, when set, also asks for the first admin's email and
	// password, and makes them (with the password's hash).
	FirstAdmin func(ctx context.Context, email, passwordHash string) error
	// Done runs once everything is saved; Overfin then finishes starting.
	Done   func()
	Logger *slog.Logger

	mu       sync.Mutex
	failures int
	done     bool
}

// Problem is what's wrong, in plain words for the operator, shown as is.
// Other errors are logged, and the page only says it failed.
type Problem string

func (p Problem) Error() string { return string(p) }

// JellyfinRequest is what the setup page sends about Jellyfin.
type JellyfinRequest struct {
	URL string
	// Username and Password are an administrator's login, for a server
	// Overfin didn't set up: used once, never kept.
	Username, Password string
	// Confirm is the ID of the server the operator saw and confirmed is
	// dedicated to Overfin.
	Confirm string
}

// JellyfinAnswer is where the Jellyfin step stands.
type JellyfinAnswer struct {
	Done    bool           `json:"done"`
	Summary string         `json:"summary,omitempty"`
	Server  JellyfinServer `json:"server"`
	// NeedsLogin: set up by someone else; only its administrator can hand
	// it over.
	NeedsLogin bool `json:"needsLogin,omitempty"`
	// Survey is what it holds, to confirm before it's handed over.
	Survey *JellyfinSurvey `json:"survey,omitempty"`
}

// JellyfinServer is the server found.
type JellyfinServer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// JellyfinSurvey is what a server holds.
type JellyfinSurvey struct {
	Users     []string `json:"users"`
	Libraries int      `json:"libraries"`
}

// setupAttempts is how many wrong codes the page takes before it stops
// answering, until Overfin restarts with a new code.
const setupAttempts = 10

// codeAlphabet leaves out letters and digits easily mistaken for others
// (0 and O, 1, I and L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewSetupCode makes a one-time setup code such as "K7QM-2XRP".
func NewSetupCode() string {
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// normalizeCode accepts a code typed in lowercase, with or without its
// dash or spaces.
func normalizeCode(c string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.ToUpper(strings.TrimSpace(c)))
}

// Handler serves the setup page, with the control panel's guards: private
// addresses only, same-origin JSON requests.
func (s *Setup) Handler() http.Handler { return privateOnly(s.handler(false)) }

// PublicHandler serves the setup page on the site's own address, to
// anyone: the one-time code is what keeps others out.
func (s *Setup) PublicHandler() http.Handler { return s.handler(true) }

func (s *Setup) handler(public bool) http.Handler {
	mux := http.NewServeMux()
	ui, _ := fs.Sub(uiFiles, "ui")
	files := http.StripPrefix("/admin/", http.FileServerFS(ui))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, ui, "setup.html")
	})
	for _, f := range []string{"setup.js", "panel.css", "logo.svg"} {
		mux.Handle("GET /admin/"+f, files)
	}
	mux.HandleFunc("GET /admin/api/setup", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := map[string]any{"jellyfin": s.Jellyfin != nil, "fixedUrl": s.FixedURL, "admin": s.FirstAdmin != nil, "site": public}
		if s.Language != nil {
			out["languages"] = lang.Choices
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/api/setup", s.save)
	return sameOrigin(headers(mux))
}

func (s *Setup) save(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code             string `json:"code"`
		URL              string `json:"url"`
		JellyfinUsername string `json:"jellyfinUsername"`
		JellyfinPassword string `json:"jellyfinPassword"`
		Confirm          string `json:"confirm"`
		Language         string `json:"language"`
		Email            string `json:"email"`
		Password         string `json:"password"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That request isn't valid."})
		return
	}
	// One attempt at a time: tests take a few seconds, and codes can't be
	// tried in parallel.
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.done:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Overfin is already set up: reload the page."})
		return
	case s.failures >= setupAttempts:
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "Too many wrong codes: restart Overfin, and use the new code in its log."})
		return
	case subtle.ConstantTimeCompare([]byte(normalizeCode(req.Code)), []byte(normalizeCode(s.Code))) != 1:
		s.failures++
		s.Logger.Warn("setup page: wrong code", "attempts", s.failures)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "That isn't the code in Overfin's log."})
		return
	}
	unusable := func(msg string) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
	}
	// Everything is checked before anything is saved.
	if s.Language != nil && !lang.Offered(req.Language) {
		unusable("Choose the default language.")
		return
	}
	var email, hash string
	if s.FirstAdmin != nil {
		email = strings.TrimSpace(req.Email)
		if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
			unusable("The admin's email isn't an email address.")
			return
		}
		var err error
		if hash, err = password.Hash(req.Password); err != nil {
			unusable(fmt.Sprintf("The admin's password must be %d to %d characters.", password.MinLength, password.MaxLength))
			return
		}
	}
	summary := "Overfin is set up."
	if s.Jellyfin != nil {
		ans, err := s.Jellyfin(r.Context(), JellyfinRequest{URL: req.URL, Username: strings.TrimSpace(req.JellyfinUsername),
			Password: req.JellyfinPassword, Confirm: req.Confirm})
		var plain Problem
		switch {
		case errors.As(err, &plain):
			unusable(string(plain))
			return
		case err != nil:
			s.Logger.Error("setup page: Jellyfin's step failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "That failed; Overfin's log says why."})
			return
		case !ans.Done:
			// The next thing to ask: an administrator's login, or a
			// confirmation of what the server holds.
			writeJSON(w, http.StatusOK, map[string]any{"jellyfin": ans})
			return
		}
		s.Jellyfin = nil // Overfin's now: a retry only asks for the rest
		summary = ans.Summary
	}
	if s.Language != nil {
		if err := s.Language(r.Context(), req.Language); err != nil {
			s.Logger.Error("setup page: saving the language failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Saving the language failed; Overfin's log says why."})
			return
		}
		s.Language = nil
	}
	if s.FirstAdmin != nil {
		if err := s.FirstAdmin(r.Context(), email, hash); err != nil {
			s.Logger.Error("setup page: making the first admin failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Saving the admin failed; Overfin's log says why."})
			return
		}
		s.Logger.Info("setup page: the first admin is made")
	}
	s.done = true
	if s.Done != nil {
		s.Done()
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary})
}
