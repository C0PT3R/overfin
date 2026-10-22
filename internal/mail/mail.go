// Package mail sends Overfin's few emails (sign-in codes, invitations)
// through the operator's SMTP server, with the standard library's SMTP
// client: plain text, UTF-8, one recipient at a time.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
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
}

// ErrAddress: an address isn't one email address.
var ErrAddress = errors.New("mail: not an email address")

// Send sends one plain-text email to one address.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("mail: the sender: %w", err)
	}
	rcpt, err := mail.ParseAddress(to)
	if err != nil || rcpt.Address != to {
		return ErrAddress
	}
	msg, err := message(from, rcpt, subject, body)
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

// message is the email's headers and body. Headers can't carry a line
// break: nothing can add one of its own.
func message(from, to *mail.Address, subject, body string) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("mail: a subject is one line")
	}
	id := make([]byte, 16)
	rand.Read(id)
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
