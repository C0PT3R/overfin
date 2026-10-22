package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"

	"github.com/overfin/server/internal/password"
)

// Setup is the first-run setup page, served until Overfin is set up: on
// the control panel's address, and with managed sign-in on the site's own
// too. It asks for what's missing: Jellyfin's address and an API key,
// tested before they're saved, and with managed sign-in the first admin's
// email and password. Only someone with the one-time code from Overfin's
// log can use it, so nobody else can claim a fresh install.
type Setup struct {
	// Code is the one-time code Overfin printed in its log (NewSetupCode).
	Code string
	// Test checks the Jellyfin server at url with key. Its error says what's
	// wrong in plain words, and is shown as is. Test and Save are nil when
	// Jellyfin is set up already.
	Test func(ctx context.Context, url, key string) (string, error)
	// Save keeps them.
	Save func(ctx context.Context, url, key string) error
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
	for _, f := range []string{"setup.js", "panel.css"} {
		mux.Handle("GET /admin/"+f, files)
	}
	mux.HandleFunc("GET /admin/api/setup", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"jellyfin": s.Test != nil, "admin": s.FirstAdmin != nil, "site": public})
	})
	mux.HandleFunc("POST /admin/api/setup", s.save)
	return sameOrigin(headers(mux))
}

func (s *Setup) save(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		URL      string `json:"url"`
		Key      string `json:"key"`
		Email    string `json:"email"`
		Password string `json:"password"`
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
	url := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	key := strings.TrimSpace(req.Key)
	summary := "Overfin is set up."
	if s.Test != nil {
		var err error
		if summary, err = s.Test(r.Context(), url, key); err != nil {
			unusable(err.Error())
			return
		}
		if err := s.Save(r.Context(), url, key); err != nil {
			s.Logger.Error("setup page: saving Jellyfin's settings failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Saving failed; Overfin's log says why."})
			return
		}
		s.Test, s.Save = nil, nil // saved: a retry only asks for the rest
		s.Logger.Info("setup page: Jellyfin is set up", "jellyfin", url)
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
