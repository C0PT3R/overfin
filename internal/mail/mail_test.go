package mail

import (
	"bufio"
	"context"
	"net"
	"net/mail"
	"strings"
	"testing"
)

// fakeSMTP accepts one email, without TLS, and returns what it received.
func fakeSMTP(t *testing.T) (port int, got chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(s string) { conn.Write([]byte(s + "\r\n")) }
		say("220 fake")
		var all strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			all.WriteString(line)
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				say("250 fake")
			case cmd == "DATA":
				say("354 go on")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					all.WriteString(l)
				}
				say("250 ok")
			case cmd == "QUIT":
				say("221 bye")
				got <- all.String()
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

func TestSend(t *testing.T) {
	port, got := fakeSMTP(t)
	s := &Sender{Host: "127.0.0.1", Port: port, TLS: "none", From: "Movies <noreply@example.org>"}
	if err := s.Send(context.Background(), "someone@example.org", "Votre code de connexion", "Code : 123456\nÀ bientôt"); err != nil {
		t.Fatal(err)
	}
	all := <-got
	for _, want := range []string{"MAIL FROM:<noreply@example.org>", "RCPT TO:<someone@example.org>",
		"From: \"Movies\" <noreply@example.org>", "Subject: Votre code de connexion",
		"Content-Type: text/plain; charset=utf-8", "Code : 123456", "=C3=80 bient=C3=B4t"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}

func TestRefusesBadInput(t *testing.T) {
	s := &Sender{Host: "127.0.0.1", Port: 1, TLS: "none", From: "noreply@example.org"}
	for _, to := range []string{"", "a@b.org, c@d.org", "Name <a@b.org>", "x\r\nBcc: e@f.org"} {
		if err := s.Send(context.Background(), to, "s", "b"); err != ErrAddress {
			t.Errorf("to %q: %v", to, err)
		}
	}
	a := &mail.Address{Address: "a@example.org"}
	if _, err := message(a, a, "two\r\nlines", "b"); err == nil {
		t.Error("a subject with a line break")
	}

}
