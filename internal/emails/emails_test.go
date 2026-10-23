package emails

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

func newWriter(t *testing.T) *Writer {
	t.Helper()
	box, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Writer{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Every language has every sentence English has.
func TestTexts(t *testing.T) {
	for lang, set := range texts {
		for key := range texts["en"] {
			if set[key] == "" {
				t.Errorf("%s: %s missing", lang, key)
			}
		}
	}
}

// Overfin's own brand: its banner on top, no "Powered by"; the reader's
// language, regional or not.
func TestWrite(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()
	m, err := w.Write(ctx, "signin", "fr-CA", Vars{Code: "123456", Link: "https://media.example.org/api/v1/signin#link=abc", Minutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Votre code de connexion" || strings.Contains(m.Subject, "123456") {
		t.Errorf("subject %q", m.Subject)
	}
	for _, want := range []string{"Voici votre code pour vous connecter à Overfin :", "123456", "https://media.example.org/api/v1/signin#link=abc", "Il est valable 15 minutes."} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, m.Text)
		}
	}
	if strings.Contains(m.Text, "Propulsé") || strings.Contains(m.HTML, "cid:overfin") {
		t.Error("Powered by, with Overfin's own brand")
	}
	if !strings.Contains(m.HTML, `src="cid:banner"`) || !strings.Contains(m.HTML, "#0c0d11") || len(m.Images) != 1 || m.Images[0].ID != "banner" {
		t.Errorf("html: %d images\n%s", len(m.Images), m.HTML)
	}
	if m, _ := w.Write(ctx, "test", "de-DE", Vars{}); m.Subject != "Overfin can send emails" {
		t.Errorf("a language without texts: %q", m.Subject)
	}
}

// The operator's brand: their name, their banner when it's a picture
// email apps show (else the name as text), and "Powered by Overfin".
func TestWriteCustom(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()
	w.Store.SetBranding(ctx, true, "zecopter")
	m, err := w.Write(ctx, "confirm", "en-CA", Vars{Code: "654321", Minutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "on zecopter:") || !strings.Contains(m.Text, "Powered by Overfin") ||
		!strings.Contains(m.HTML, ">zecopter</span>") || len(m.Images) != 1 || m.Images[0].ID != "overfin" {
		t.Errorf("no banner: %d images\n%s\n%s", len(m.Images), m.Text, m.HTML)
	}
	w.Store.SetBrandingFile(ctx, "banner-dark", "image/png", overfinLight, time.Now())
	m, _ = w.Write(ctx, "confirm", "en-CA", Vars{Code: "654321", Minutes: 15})
	if len(m.Images) != 2 || !strings.Contains(m.HTML, `src="cid:banner"`) || !strings.Contains(m.HTML, `src="cid:overfin"`) {
		t.Errorf("with a banner: %d images", len(m.Images))
	}
}

// The operator's templates: for one language, or every one; one that
// fails leaves Overfin's in its place.
func TestOperatorTemplates(t *testing.T) {
	w := newWriter(t)
	w.Dir = t.TempDir()
	ctx := context.Background()
	os.WriteFile(filepath.Join(w.Dir, "signin.fr.txt"), []byte(`{{define "subject"}}Code pour {{.Site}}{{end}}{{define "content"}}Code : {{.Code}}{{end}}{{define "why"}}.{{end}}`), 0o600)
	if m, _ := w.Write(ctx, "signin", "fr-CA", Vars{Code: "111111"}); m.Subject != "Code pour Overfin" || !strings.HasPrefix(m.Text, "Code : 111111") {
		t.Errorf("French: %q %q", m.Subject, m.Text)
	}
	if m, _ := w.Write(ctx, "signin", "en-US", Vars{Code: "111111"}); m.Subject != "Your sign-in code" {
		t.Errorf("English: %q", m.Subject)
	}
	if w.Origin("signin", ".txt", "fr-CA") != "signin.fr.txt" || w.Origin("signin", ".html", "fr-CA") != "" {
		t.Error("origins")
	}
	os.WriteFile(filepath.Join(w.Dir, "signin.html"), []byte(`{{define "content"}}{{.Nope}}{{end}}`), 0o600)
	if m, err := w.Write(ctx, "signin", "en-US", Vars{Code: "222222"}); err != nil || !strings.Contains(m.HTML, "222222") {
		t.Errorf("a broken template: %v", err)
	}
}
