package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"

	"github.com/overfin/server/internal/introskipper"
	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/password"
)

// Setup is the first-run setup wizard, served until Overfin is set up: on
// the control panel's address, and with managed sign-in on the site's own
// too. One step per screen, each saved as it's done, for what's missing:
// the default language; Overfin's own Jellyfin (SECURITY.md rule 67), which
// it sets up if it's fresh, and otherwise shows, then takes only when its
// administrator hands it over; TMDB's key; and with managed sign-in the
// first admin. Only someone with the one-time code from Overfin's log can
// use it, so nobody else can claim a fresh install.
type Setup struct {
	// Code is the one-time code Overfin printed in its log (NewSetupCode).
	Code string
	// Language, when set, asks for the default language (a lang.Choices
	// tag), and saves it.
	Language func(ctx context.Context, tag string) error
	// Jellyfin takes one step towards Overfin's Jellyfin, and saves it once
	// it's Overfin's (Done). Its error says what's wrong in plain words, and
	// is shown as is. Nil when Jellyfin is set up already.
	Jellyfin func(ctx context.Context, req JellyfinRequest) (JellyfinAnswer, error)
	// FixedURL is Jellyfin's address when the configuration file sets it:
	// the page doesn't ask for it then.
	FixedURL string
	// TMDB, when set, offers TMDB's key: it tests and saves one, and says
	// so. The step may be skipped.
	TMDB func(ctx context.Context, key string) (string, error)
	// IntroSkipper, when set, offers to install Intro Skipper on Overfin's
	// Jellyfin, once it has one. The step may be skipped.
	IntroSkipper *introskipper.Installer
	// FirstAdmin, when set, asks for the first admin's email and password,
	// and makes them (with the password's hash).
	FirstAdmin func(ctx context.Context, email, passwordHash string) error
	// Done runs once the wizard is finished; Overfin then finishes
	// starting.
	Done   func()
	Logger *slog.Logger

	mu       sync.Mutex
	steps    []string        // the wizard's steps, fixed on first use
	saved    map[string]bool // the steps done
	failures int
	done     bool
}

// The wizard's steps, in order; those with nothing to ask are left out.
const (
	stepCode     = "code"
	stepLanguage = "language"
	stepJellyfin = "jellyfin"
	stepTMDB     = "tmdb"
	stepSkipper  = "introskipper"
	stepAdmin    = "admin"
	stepDone     = "done"
)

// optional are the steps that may be skipped.
var optional = map[string]bool{stepTMDB: true, stepSkipper: true}

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
		type step struct {
			ID       string `json:"id"`
			Done     bool   `json:"done"`
			Optional bool   `json:"optional,omitempty"`
		}
		var steps []step
		for _, id := range s.stepList() {
			steps = append(steps, step{id, s.saved[id], optional[id]})
		}
		out := map[string]any{"steps": steps, "fixedUrl": s.FixedURL, "site": public, "finished": s.done}
		if s.Language != nil {
			out["languages"] = lang.Choices
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/api/setup/code", s.step(func(w http.ResponseWriter, r *http.Request, _ json.RawMessage) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /admin/api/setup/language", s.step(s.language))
	mux.HandleFunc("POST /admin/api/setup/jellyfin", s.step(s.jellyfin))
	mux.HandleFunc("POST /admin/api/setup/tmdb", s.step(s.tmdb))
	mux.HandleFunc("POST /admin/api/setup/introskipper", s.step(s.introSkipper))
	mux.HandleFunc("POST /admin/api/setup/admin", s.step(s.admin))
	mux.HandleFunc("POST /admin/api/setup/finish", s.step(s.finish))
	return sameOrigin(headers(mux))
}

// stepList is the wizard's steps: those with something to ask, fixed on
// first use, so a step stays (done) once saved and Back still shows it.
// Callers hold mu.
func (s *Setup) stepList() []string {
	if s.steps == nil {
		s.steps = []string{stepCode}
		for _, st := range []struct {
			id  string
			ask bool
		}{{stepLanguage, s.Language != nil}, {stepJellyfin, s.Jellyfin != nil},
			{stepTMDB, s.TMDB != nil}, {stepSkipper, s.IntroSkipper != nil}, {stepAdmin, s.FirstAdmin != nil}} {
			if st.ask {
				s.steps = append(s.steps, st.id)
			}
		}
		s.steps = append(s.steps, stepDone)
		s.saved = map[string]bool{}
	}
	return s.steps
}

// step serves one of the wizard's steps: one request at a time (tests take
// a few seconds, and codes can't be tried in parallel), each with the
// one-time code. The handler gets the request's other fields.
func (s *Setup) step(next func(w http.ResponseWriter, r *http.Request, body json.RawMessage)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil || json.Unmarshal(body, &req) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That request isn't valid."})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stepList()
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
		next(w, r, body)
	}
}

// decodeStep reads a step's fields; the code is allowed alongside.
func decodeStep(w http.ResponseWriter, body json.RawMessage, v any) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) == nil {
		delete(fields, "code")
		body, _ = json.Marshal(fields)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That request isn't valid."})
		return false
	}
	return true
}

// unusable answers what's wrong with a step's answer, in plain words.
func unusable(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
}

// failed answers an error from a step: as is when it's a Problem, else
// logged.
func (s *Setup) failed(w http.ResponseWriter, err error, what string) {
	var plain Problem
	if errors.As(err, &plain) {
		unusable(w, string(plain))
		return
	}
	s.Logger.Error("setup page: "+what+" failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "That failed; Overfin's log says why."})
}

func (s *Setup) language(w http.ResponseWriter, r *http.Request, body json.RawMessage) {
	var req struct {
		Language string `json:"language"`
	}
	if !decodeStep(w, body, &req) {
		return
	}
	if s.Language == nil {
		http.NotFound(w, r)
		return
	}
	if !lang.Offered(req.Language) {
		unusable(w, "Choose the default language.")
		return
	}
	if err := s.Language(r.Context(), req.Language); err != nil {
		s.failed(w, err, "saving the language")
		return
	}
	s.saved[stepLanguage] = true
	w.WriteHeader(http.StatusNoContent)
}

func (s *Setup) jellyfin(w http.ResponseWriter, r *http.Request, body json.RawMessage) {
	var req struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if !decodeStep(w, body, &req) {
		return
	}
	if s.Jellyfin == nil || s.saved[stepJellyfin] {
		if s.saved[stepJellyfin] {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Overfin has its Jellyfin already."})
			return
		}
		http.NotFound(w, r)
		return
	}
	ans, err := s.Jellyfin(r.Context(), JellyfinRequest{URL: req.URL, Username: strings.TrimSpace(req.Username),
		Password: req.Password, Confirm: req.Confirm})
	if err != nil {
		s.failed(w, err, "Jellyfin's step")
		return
	}
	// Not done: the next thing to ask, an administrator's login or a
	// confirmation of what the server holds.
	if ans.Done {
		s.saved[stepJellyfin] = true
	}
	writeJSON(w, http.StatusOK, ans)
}

func (s *Setup) tmdb(w http.ResponseWriter, r *http.Request, body json.RawMessage) {
	var req struct {
		Key string `json:"key"`
	}
	if !decodeStep(w, body, &req) {
		return
	}
	if s.TMDB == nil {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		unusable(w, "Paste TMDB's key first, or skip this step.")
		return
	}
	summary, err := s.TMDB(r.Context(), req.Key)
	if err != nil {
		s.failed(w, err, "TMDB's step")
		return
	}
	s.saved[stepTMDB] = true
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary})
}

// introSkipper starts Intro Skipper's install when asked, and says where
// it stands; the page asks again until it's done.
func (s *Setup) introSkipper(w http.ResponseWriter, r *http.Request, body json.RawMessage) {
	var req struct {
		Start bool `json:"start"`
	}
	if !decodeStep(w, body, &req) {
		return
	}
	if s.IntroSkipper == nil {
		http.NotFound(w, r)
		return
	}
	if req.Start {
		if err := s.IntroSkipper.Start(); err != nil {
			unusable(w, err.Error()) // only ever a Problem
			return
		}
	}
	p := s.IntroSkipper.Progress()
	if p.Done {
		s.saved[stepSkipper] = true
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Setup) admin(w http.ResponseWriter, r *http.Request, body json.RawMessage) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeStep(w, body, &req) {
		return
	}
	if s.FirstAdmin == nil || s.saved[stepAdmin] {
		if s.saved[stepAdmin] {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "The first admin is made already."})
			return
		}
		http.NotFound(w, r)
		return
	}
	email := strings.TrimSpace(req.Email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		unusable(w, "The admin's email isn't an email address.")
		return
	}
	hash, err := password.Hash(req.Password)
	if err != nil {
		unusable(w, fmt.Sprintf("The admin's password must be %d to %d characters.", password.MinLength, password.MaxLength))
		return
	}
	if err := s.FirstAdmin(r.Context(), email, hash); err != nil {
		s.failed(w, err, "making the first admin")
		return
	}
	s.Logger.Info("setup page: the first admin is made")
	s.saved[stepAdmin] = true
	w.WriteHeader(http.StatusNoContent)
}

// finish ends the wizard once every step that can't be skipped is done.
func (s *Setup) finish(w http.ResponseWriter, r *http.Request, body json.RawMessage) {
	if !decodeStep(w, body, &struct{}{}) {
		return
	}
	if s.IntroSkipper != nil && s.IntroSkipper.Progress().Running {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Intro Skipper is still being installed.", "step": stepSkipper})
		return
	}
	for _, id := range s.steps {
		if id != stepCode && id != stepDone && !optional[id] && !s.saved[id] {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "A step isn't done yet.", "step": id})
			return
		}
	}
	s.done = true
	if s.Done != nil {
		s.Done()
	}
	w.WriteHeader(http.StatusNoContent)
}
