package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/overfin/server/internal/config"
)

// Mail is where the panel reads and changes the SMTP server sign-in codes
// go through.
type Mail interface {
	// Mail is the SMTP server's settings, whether it has a password, and
	// whether the configuration file sets them (then the panel can't).
	Mail() (m config.Mail, hasPassword, inFile bool)
	// SetMail tests (with an email to testTo) and applies new settings;
	// an empty host turns email off. Its error says what's wrong in plain
	// words.
	SetMail(ctx context.Context, m config.Mail, password, testTo string) (string, error)
	// MailTemplates lists Overfin's emails with whose templates write
	// them, in the default language, and the folder the operator's go in.
	MailTemplates() (list []MailTemplate, dir string)
	// SendSample sends one of them to an address, with a made-up code.
	SendSample(ctx context.Context, name, to string) error
}

// MailTemplate is one of Overfin's emails: whose templates write it, ""
// for Overfin's, else the operator's file's name.
type MailTemplate struct {
	Name string `json:"name"`
	HTML string `json:"html"`
	Text string `json:"text"`
}

type mailJSON struct {
	config.Mail
	HasPassword bool `json:"hasPassword"`
	InFile      bool `json:"inFile"`
}

func (p *Panel) mailTemplates(w http.ResponseWriter, r *http.Request, _ *session) {
	list, dir := p.Mail.MailTemplates()
	writeJSON(w, http.StatusOK, map[string]any{"emails": list, "dir": dir})
}

// sendSample sends one email to the admin asking, as people get it.
func (p *Panel) sendSample(w http.ResponseWriter, r *http.Request, s *session) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	if err := p.Mail.SendSample(r.Context(), r.PathValue("name"), s.name); err != nil {
		invalid(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": "Sent to " + s.name + "."})
}

func (p *Panel) mail(w http.ResponseWriter, r *http.Request, _ *session) {
	m, hasPassword, inFile := p.Mail.Mail()
	writeJSON(w, http.StatusOK, mailJSON{Mail: m, HasPassword: hasPassword, InFile: inFile})
}

// setMail sends the test email to the admin making the change: their
// email is their name with managed sign-in.
func (p *Panel) setMail(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		config.Mail
		Password string `json:"password"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not valid settings")
		return
	}
	summary, err := p.Mail.SetMail(r.Context(), req.Mail, req.Password, s.name)
	if err != nil {
		invalid(w, err.Error())
		return
	}
	m, hasPassword, inFile := p.Mail.Mail()
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "mail": mailJSON{Mail: m, HasPassword: hasPassword, InFile: inFile}})
}
