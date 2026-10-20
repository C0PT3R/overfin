// Package invites is the invites module: links that let people set up an
// account, made in the control panel from invite templates.
//
// Someone opens their invitation link, which leaves a cookie in their
// browser and sends them on to log in; when their account is set up, the
// invitation shapes it (its account template, when its access ends, a label)
// and counts as used. With "accounts only by invitation" on, nobody gets an
// account without one, except people bringing their previous account over,
// whose old account is proof enough.
package invites

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/C0PT3R/overfin-gateway/module"
)

//go:embed migrations/*.sql ui/panel.js
var files embed.FS

// cookieName carries an opened invitation's code to the account's creation.
const cookieName = "__Host-overfin-invite"

// codePattern is a code's form: 16 random bytes, base64url.
var codePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// Module is the invites module.
type Module struct{}

func (Module) Info() module.Info {
	migrations, _ := fs.Sub(files, "migrations")
	ui, _ := fs.Sub(files, "ui")
	return module.Info{
		Name:        "invites",
		Description: "Invitation links that let people set up an account, made from invite templates",
		Settings: []module.Setting{{
			Key:   "required",
			Label: "Accounts only by invitation",
			Hint:  "Without one, nobody can set up an account; bringing a previous account still works",
		}},
		Migrations: migrations,
		AdminUI:    ui,
	}
}

func (Module) Setup(h module.Host) error {
	a := admin{h: h, db: h.DB()}
	if err := h.HandleLink("GET /api/v1/invite", a.open); err != nil {
		return err
	}
	for pattern, fn := range map[string]http.HandlerFunc{
		"GET /account-templates": a.accountTemplates,
		"GET /templates":         a.templates,
		"POST /templates":        a.saveTemplate,
		"PUT /templates/{id}":    a.saveTemplate,
		"DELETE /templates/{id}": a.deleteTemplate,
		"GET /invites":           a.invites,
		"POST /invites":          a.createInvite,
		"DELETE /invites/{id}":   a.deleteInvite,
	} {
		if err := h.HandleAdmin(pattern, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---- The link and the account gate ------------------------------------------

// open is where an invitation link leads: it keeps the code in a cookie and
// sends the person on to the site, which has them log in.
func (a admin) open(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	inv, err := usable(r.Context(), a.db, code)
	if err != nil {
		a.h.Logger().Error("reading an invitation failed", "err", err)
		http.Error(w, "Something went wrong; please try again later.", http.StatusInternalServerError)
		return
	}
	if inv == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("This invitation has expired or has already been used. Ask the person who sent it for a new one.\n"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: code, Path: "/", MaxAge: 24 * 60 * 60,
		// Lax: it must survive the trip through the identity provider.
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// NewAccount shapes a new account by the visitor's invitation, or refuses
// it when invitations are required and there's none.
func (Module) NewAccount(ctx context.Context, h module.Host, r *http.Request) (*module.AccountPlan, error) {
	var inv *invite
	if c, err := r.Cookie(cookieName); err == nil {
		if inv, err = usable(ctx, h.DB(), c.Value); err != nil {
			return nil, err
		}
	}
	if inv != nil {
		ok, err := templateExists(ctx, h, inv.AccountTemplate)
		if err != nil {
			return nil, err
		}
		if !ok {
			h.Logger().Warn("an invitation's account template no longer exists; make a new invitation", "invite", inv.ID)
			inv = nil
		}
	}
	if inv == nil {
		if h.Setting("required") == "on" && !module.Adopting(ctx) {
			return nil, &module.Refusal{Type: module.InvitationRequired}
		}
		return nil, nil
	}
	plan := &module.AccountPlan{Template: inv.AccountTemplate, Label: inv.Label, Ref: inv.ID}
	if inv.AccessDays > 0 {
		plan.Expires = time.Now().AddDate(0, 0, inv.AccessDays)
	}
	return plan, nil
}

// AccountCreated counts the invitation as used. It runs under the core's
// account-creation lock, so a single-use invitation can't be used twice.
func (Module) AccountCreated(ctx context.Context, h module.Host, _ *http.Request, accountID string, plan *module.AccountPlan) error {
	if plan == nil {
		return nil
	}
	res, err := h.DB().ExecContext(ctx, `
		INSERT INTO invites_uses (invite_id, account_id, used_at)
		SELECT id, ?, ? FROM invites
		WHERE id = ? AND expires_at > ?
			AND (SELECT count(*) FROM invites_uses WHERE invite_id = invites.id) < max_uses`,
		accountID, time.Now().Unix(), plan.Ref, time.Now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("invites: the invitation ran out meanwhile")
	}
	return nil
}

type invite struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	AccountTemplate string `json:"accountTemplate"`
	AccessDays      int    `json:"accessDays"`
	MaxUses         int    `json:"maxUses"`
	Uses            int    `json:"uses"`
	ExpiresAt       string `json:"expiresAt"`
	CreatedAt       string `json:"createdAt"`
	expires         int64
}

const inviteColumns = `id, label, account_template, access_days, max_uses,
	(SELECT count(*) FROM invites_uses WHERE invite_id = invites.id), expires_at, created_at`

func scanInvite(sc interface{ Scan(...any) error }) (*invite, error) {
	var inv invite
	var created int64
	if err := sc.Scan(&inv.ID, &inv.Label, &inv.AccountTemplate, &inv.AccessDays, &inv.MaxUses, &inv.Uses,
		&inv.expires, &created); err != nil {
		return nil, err
	}
	inv.ExpiresAt = time.Unix(inv.expires, 0).UTC().Format(time.RFC3339)
	inv.CreatedAt = time.Unix(created, 0).UTC().Format(time.RFC3339)
	return &inv, nil
}

// usable returns the invitation a code opens, or nil when it opens none that
// still works.
func usable(ctx context.Context, db *sql.DB, code string) (*invite, error) {
	if !codePattern.MatchString(code) {
		return nil, nil
	}
	inv, err := scanInvite(db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE code_hash = ?`, hash(code)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if inv.expires <= time.Now().Unix() || inv.Uses >= inv.MaxUses {
		return nil, nil
	}
	return inv, nil
}

func templateExists(ctx context.Context, h module.Host, id string) (bool, error) {
	ts, err := h.Templates(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range ts {
		if t.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func hash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// ---- The control panel ------------------------------------------------------

type admin struct {
	h  module.Host
	db *sql.DB
}

func (a admin) fail(w http.ResponseWriter, err error) {
	a.h.Logger().Error("invites: request failed", "err", err)
	module.Problem(w, module.Internal, "")
}

func (a admin) accountTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := a.h.Templates(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	type tj struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		MaxStreams  int    `json:"maxStreams"`
		MaxProfiles int    `json:"maxProfiles"`
	}
	out := []tj{}
	for _, t := range ts {
		out = append(out, tj{t.ID, t.Name, t.MaxStreams, t.MaxProfiles})
	}
	module.JSON(w, map[string]any{"items": out})
}

type template struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	AccountTemplate string `json:"accountTemplate"`
	AccessDays      int    `json:"accessDays"`
	ValidDays       int    `json:"validDays"`
	MaxUses         int    `json:"maxUses"`
}

func (a admin) templates(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT id, name, account_template, access_days, valid_days, max_uses FROM invites_templates ORDER BY name`)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []template{}
	for rows.Next() {
		var t template
		if err := rows.Scan(&t.ID, &t.Name, &t.AccountTemplate, &t.AccessDays, &t.ValidDays, &t.MaxUses); err != nil {
			a.fail(w, err)
			return
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}
	module.JSON(w, map[string]any{"items": out})
}

// saveTemplate creates an invite template (POST) or changes one (PUT).
func (a admin) saveTemplate(w http.ResponseWriter, r *http.Request) {
	var t template
	if !decode(w, r, &t) {
		return
	}
	exists, err := templateExists(r.Context(), a.h, t.AccountTemplate)
	if err != nil {
		a.fail(w, err)
		return
	}
	switch {
	case !plain(t.Name, 64) || t.Name == "":
		module.Problem(w, module.InvalidRequest, "a name is plain text, 1 to 64 characters")
		return
	case !exists:
		module.Problem(w, module.InvalidRequest, "unknown account template")
		return
	case t.AccessDays < 0 || t.AccessDays > 3650:
		module.Problem(w, module.InvalidRequest, "access: 0 (no end) to 3650 days")
		return
	case t.ValidDays < 1 || t.ValidDays > 365:
		module.Problem(w, module.InvalidRequest, "a link works 1 to 365 days")
		return
	case t.MaxUses < 1 || t.MaxUses > 1000:
		module.Problem(w, module.InvalidRequest, "uses: 1 to 1000")
		return
	}
	if id := r.PathValue("id"); id != "" {
		var res sql.Result
		res, err = a.db.ExecContext(r.Context(), `
			UPDATE invites_templates SET name = ?, account_template = ?, access_days = ?, valid_days = ?, max_uses = ?
			WHERE id = ?`, t.Name, t.AccountTemplate, t.AccessDays, t.ValidDays, t.MaxUses, id)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				module.Problem(w, module.NotFound, "")
				return
			}
		}
		t.ID = id
	} else {
		t.ID = newID("itmpl_")
		_, err = a.db.ExecContext(r.Context(), `
			INSERT INTO invites_templates (id, name, account_template, access_days, valid_days, max_uses)
			VALUES (?, ?, ?, ?, ?, ?)`, t.ID, t.Name, t.AccountTemplate, t.AccessDays, t.ValidDays, t.MaxUses)
	}
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		module.Problem(w, module.InvalidRequest, "another invite template has that name")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	module.JSON(w, t)
}

func (a admin) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM invites_templates WHERE id = ?`, r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	module.NoContent(w)
}

func (a admin) invites(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT `+inviteColumns+` FROM invites ORDER BY created_at DESC, id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*invite{}
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			a.fail(w, err)
			return
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}
	module.JSON(w, map[string]any{"items": out})
}

// createInvite makes an invitation from a template, for someone. Its link
// is given this once: only the code's hash is kept.
func (a admin) createInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Template string `json:"template"`
		Label    string `json:"label"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !plain(req.Label, 64) {
		module.Problem(w, module.InvalidRequest, "who it's for is plain text, 64 characters at most")
		return
	}
	var t template
	err := a.db.QueryRowContext(r.Context(), `
		SELECT account_template, access_days, valid_days, max_uses FROM invites_templates WHERE id = ?`, req.Template).
		Scan(&t.AccountTemplate, &t.AccessDays, &t.ValidDays, &t.MaxUses)
	if errors.Is(err, sql.ErrNoRows) {
		module.Problem(w, module.InvalidRequest, "unknown invite template")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	code := newCode()
	now := time.Now()
	id := newID("inv_")
	if _, err := a.db.ExecContext(r.Context(), `
		INSERT INTO invites (id, code_hash, label, account_template, access_days, max_uses, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, hash(code), req.Label, t.AccountTemplate, t.AccessDays, t.MaxUses,
		now.AddDate(0, 0, t.ValidDays).Unix(), now.Unix()); err != nil {
		a.fail(w, err)
		return
	}
	inv, err := scanInvite(a.db.QueryRowContext(r.Context(), `SELECT `+inviteColumns+` FROM invites WHERE id = ?`, id))
	if err != nil {
		a.fail(w, err)
		return
	}
	module.JSON(w, map[string]any{"invite": inv, "link": a.h.SiteURL() + "/api/v1/invite?code=" + code})
}

func (a admin) deleteInvite(w http.ResponseWriter, r *http.Request) {
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM invites WHERE id = ?`, r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	module.NoContent(w)
}

// ---- Helpers ----------------------------------------------------------------

// decode reads a small JSON body strictly, answering the problem itself.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		module.Problem(w, module.InvalidRequest, "")
		return false
	}
	return true
}

// plain reports whether s is plain text of at most max characters.
func plain(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max || strings.TrimSpace(s) != s {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c) {
			return false
		}
	}
	return true
}

func newCode() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
