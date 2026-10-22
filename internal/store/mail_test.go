package store

import (
	"context"
	"testing"
)

func TestMailSettings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, _, ok, err := s.Mail(ctx); ok || err != nil {
		t.Fatalf("none yet: %v %v", ok, err)
	}
	if err := s.SetMail(ctx, []byte(`{"host":"smtp.example.org"}`), "secret"); err != nil {
		t.Fatal(err)
	}
	raw, pw, ok, err := s.Mail(ctx)
	if !ok || err != nil || string(raw) != `{"host":"smtp.example.org"}` || pw != "secret" {
		t.Fatalf("saved: %s %q %v %v", raw, pw, ok, err)
	}
	// Sealed: the password isn't in the database as is.
	var stored []byte
	s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'mail-password'`).Scan(&stored)
	if string(stored) == "secret" {
		t.Error("the password is stored in clear")
	}
	if err := s.ForgetMail(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := s.Mail(ctx); ok {
		t.Error("still there after ForgetMail")
	}
}
