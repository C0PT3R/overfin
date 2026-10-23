// Package mail sends Overfin's few emails (sign-in codes, invitations)
// through the operator's SMTP server, with the standard library's SMTP
// client: plain text, UTF-8, one recipient at a time.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sender is an SMTP server to send through.
type Sender struct {
	Host string
	Port int
	// TLS is "starttls" (usually port 587), "tls" (usually 465), or "none"
	// (a relay on the local network; no password is sent then).
	TLS string
	// From is the sender, "address" or "Name <address>".
	From     string
	Username string
	Password string
	// Timeout bounds a whole sending; 30 seconds when zero.
	Timeout time.Duration
	// Name, when set, gives the sender's display name instead of From's
	// (the site's own name, with the operator's branding); "" keeps From's.
	Name func() string
}

// ErrAddress: an address isn't one email address.
var ErrAddress = errors.New("mail: not an email address")

// Message is one email: its subject, its text, and optionally an HTML
// version with the images it shows, sent inside the email (so email apps
// show them without asking to load remote images).
type Message struct {
	Subject string
	Text    string
	HTML    string
	Images  []Image
}

// Image is a picture an HTML message shows as <img src="cid:ID">.
type Image struct {
	ID          string
	ContentType string
	Data        []byte
}

// Send sends one plain-text email to one address.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	return s.SendMessage(ctx, to, Message{Subject: subject, Text: body})
}

// SendMessage sends one email to one address.
func (s *Sender) SendMessage(ctx context.Context, to string, m Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("mail: the sender: %w", err)
	}
	if s.Name != nil {
		if name := s.Name(); name != "" {
			from.Name = name
		}
	}
	rcpt, err := mail.ParseAddress(to)
	if err != nil || rcpt.Address != to {
		return ErrAddress
	}
	msg, err := message(from, rcpt, m)
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	var conn net.Conn
	if s.TLS == "tls" {
		conn, err = (&tls.Dialer{Config: &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: connecting to %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if s.TLS == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: STARTTLS: %w", err)
		}
	}
	if s.Username != "" && s.TLS != "none" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("mail: the SMTP server refused the username or password: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	return c.Quit()
}

// message is the email's headers and body: plain text alone, or text and
// HTML as alternatives, the HTML with its images. Headers can't carry a
// line break: nothing can add one of its own.
func message(from, to *mail.Address, m Message) ([]byte, error) {
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, errors.New("mail: a subject is one line")
	}
	for _, img := range m.Images {
		if !cid.MatchString(img.ID) || !strings.HasPrefix(img.ContentType, "image/") || strings.ContainsAny(img.ContentType, "\r\n") {
			return nil, errors.New("mail: an image needs a plain ID and an image type")
		}
	}
	id := make([]byte, 16)
	rand.Read(id)
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	if m.HTML == "" {
		header("Content-Type", "text/plain; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, m.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	alt := multipart.NewWriter(&b)
	header("Content-Type", `multipart/alternative; boundary="`+alt.Boundary()+`"`)
	b.WriteString("\r\n")
	part, err := alt.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return nil, err
	}
	if err := writeQP(part, m.Text); err != nil {
		return nil, err
	}
	// The HTML and its images, together.
	var related bytes.Buffer
	rel := multipart.NewWriter(&related)
	part, err = alt.CreatePart(textproto.MIMEHeader{"Content-Type": {`multipart/related; boundary="` + rel.Boundary() + `"`}})
	if err != nil {
		return nil, err
	}
	html, err := rel.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return nil, err
	}
	if err := writeQP(html, m.HTML); err != nil {
		return nil, err
	}
	for _, img := range m.Images {
		w, err := rel.CreatePart(textproto.MIMEHeader{"Content-Type": {img.ContentType},
			"Content-Transfer-Encoding": {"base64"}, "Content-ID": {"<" + img.ID + ">"},
			"Content-Disposition": {"inline"}})
		if err != nil {
			return nil, err
		}
		if err := writeBase64(w, img.Data); err != nil {
			return nil, err
		}
	}
	if err := rel.Close(); err != nil {
		return nil, err
	}
	if _, err := part.Write(related.Bytes()); err != nil {
		return nil, err
	}
	if err := alt.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// cid is what an image's ID looks like: plain, so it can't break a header.
var cid = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}(@[A-Za-z0-9.-]{1,64})?$`)

// writeQP writes text as quoted-printable, with CRLF line ends.
func writeQP(w io.Writer, text string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return err
	}
	return qp.Close()
}

// writeBase64 writes data as base64, in lines of 76 characters.
func writeBase64(w io.Writer, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		if _, err := io.WriteString(w, enc[:76]+"\r\n"); err != nil {
			return err
		}
		enc = enc[76:]
	}
	_, err := io.WriteString(w, enc+"\r\n")
	return err
}
