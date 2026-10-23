// Package emails writes Overfin's emails: each one from a layout (the
// site's brand on top, the message, why it came, and "Powered by Overfin"
// with the operator's own brand) and its own template, in HTML and in
// plain text, with its sentences in the reader's language.
//
// Operators replace any template with a file of their own, of the same
// name, in a folder (Dir, mail-templates next to the database): name.html
// or name.txt for every language, name.fr.html for French only, and
// name.fr-CA.html for Canadian French. Their files get the same data as
// Overfin's, and are read again for every email, so a change needs no
// restart.
package emails

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	texttemplate "text/template"

	"github.com/BurntSushi/toml"
	"github.com/overfin/server/internal/mail"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/theme"
)

//go:embed templates texts
var builtin embed.FS

//go:embed overfin-banner-light.png
var overfinLight []byte

//go:embed overfin-banner-dark.png
var overfinDark []byte

// Names are the emails Overfin sends.
var Names = []string{"signin", "confirm", "test"}

// texts are the built-in sentences, by language ("en", "fr") then key
// ("poweredBy", "signin.subject"…).
var texts = func() map[string]map[string]string {
	out := map[string]map[string]string{}
	entries, _ := fs.ReadDir(builtin, "texts")
	for _, e := range entries {
		var raw map[string]any
		if _, err := toml.DecodeFS(builtin, "texts/"+e.Name(), &raw); err != nil {
			panic(err) // embedded: checked by the tests
		}
		flat := map[string]string{}
		for k, v := range raw {
			switch v := v.(type) {
			case string:
				flat[k] = v
			case map[string]any:
				for k2, v2 := range v {
					flat[k+"."+k2], _ = v2.(string)
				}
			}
		}
		out[strings.TrimSuffix(e.Name(), ".toml")] = flat
	}
	return out
}()

// Writer writes emails.
type Writer struct {
	// Dir is the operator's own templates; "" or missing, none.
	Dir    string
	Store  *store.Store
	Logger *slog.Logger
}

// picture is an image an email shows, with its size there.
type picture struct {
	Width, Height int
}

// data is what every template gets.
type data struct {
	Lang    string
	Site    string
	Subject string
	// Custom is true with the operator's own brand: "Powered by Overfin"
	// then shows, with Overfin's banner.
	Custom  bool
	Banner  *picture
	Overfin picture
	// Light and Dark are the theme's colors (theme.css), by name.
	Light, Dark map[string]string
	// Code, Link and Minutes are a sign-in or confirmation code's.
	Code    string
	Link    string
	Minutes int
}

// Vars are an email's own values.
type Vars struct {
	Code    string
	Link    string
	Minutes int
}

// Write writes an email in a language (a regional tag such as "fr-CA").
// When the operator's own templates fail, it says why in the log and
// writes it with Overfin's.
func (w *Writer) Write(ctx context.Context, name, lang string, v Vars) (mail.Message, error) {
	m, err := w.write(ctx, name, lang, v)
	if err != nil && w.Dir != "" {
		w.Logger.Warn("an email template of yours failed: Overfin's was used", "email", name, "err", err)
		return (&Writer{Store: w.Store, Logger: w.Logger}).write(ctx, name, lang, v)
	}
	return m, err
}

func (w *Writer) write(ctx context.Context, name, lang string, v Vars) (mail.Message, error) {
	br, err := w.Store.Branding(ctx)
	if err != nil {
		return mail.Message{}, err
	}
	d := data{Lang: lang, Site: brandName(br), Custom: br.On, Light: theme.Colors(true), Dark: theme.Colors(false),
		Code: v.Code, Link: v.Link, Minutes: v.Minutes}
	var images []mail.Image
	// The banner on the dark header: the operator's, as a picture email
	// apps show (not SVG), else their name as text; Overfin's otherwise.
	switch {
	case !br.On:
		d.Banner = sizeOf(overfinLight, 32)
		images = append(images, mail.Image{ID: "banner", ContentType: "image/png", Data: overfinLight})
	default:
		if f, ok, err := w.Store.BrandingFileOf(ctx, "banner-dark"); err == nil && ok {
			if p := sizeOf(f.Data, 32); p != nil {
				d.Banner = p
				images = append(images, mail.Image{ID: "banner", ContentType: f.ContentType, Data: f.Data})
			}
		}
		d.Overfin = *sizeOf(overfinDark, 16)
		images = append(images, mail.Image{ID: "overfin", ContentType: "image/png", Data: overfinDark})
	}
	t := w.translator(lang, d)
	subject, text, err := w.text(name, lang, t, d)
	if err != nil {
		return mail.Message{}, err
	}
	d.Subject = subject
	html, err := w.html(name, lang, t, d)
	if err != nil {
		return mail.Message{}, err
	}
	return mail.Message{Subject: subject, Text: text, HTML: html, Images: images}, nil
}

func brandName(br store.Branding) string {
	if br.On && br.Name != "" {
		return br.Name
	}
	return "Overfin"
}

// sizeOf is a picture's size shown height pixels tall; nil when it isn't a
// picture email apps show.
func sizeOf(b []byte, height int) *picture {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Height == 0 {
		return nil
	}
	return &picture{Width: cfg.Width * height / cfg.Height, Height: height}
}

// translator is the "t" function: a sentence in the language (else in
// English), with {site} and {minutes} filled in.
func (w *Writer) translator(lang string, d data) func(string) string {
	set := texts[strings.SplitN(lang, "-", 2)[0]]
	return func(key string) string {
		s, ok := set[key]
		if !ok {
			s = texts["en"][key]
		}
		return strings.NewReplacer("{site}", d.Site, "{minutes}", fmt.Sprint(d.Minutes)).Replace(s)
	}
}

// file is a template's source: the operator's for the language, or for
// every language, else Overfin's.
func (w *Writer) file(name, ext, lang string) (src, from string, err error) {
	if w.Dir != "" {
		base := strings.SplitN(lang, "-", 2)[0]
		for _, n := range []string{name + "." + lang + ext, name + "." + base + ext, name + ext} {
			b, err := os.ReadFile(filepath.Join(w.Dir, n))
			if err == nil {
				return string(b), n, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return "", "", err
			}
		}
	}
	b, err := builtin.ReadFile("templates/" + name + ext)
	return string(b), "", err
}

// Origin says whose a template is: the operator's file's name, or "" for
// Overfin's.
func (w *Writer) Origin(name, ext, lang string) string {
	_, from, _ := w.file(name, ext, lang)
	return from
}

// text writes the subject and the plain-text version.
func (w *Writer) text(name, lang string, t func(string) string, d data) (string, string, error) {
	funcs := texttemplate.FuncMap{"t": t}
	tpl := texttemplate.New("layout").Funcs(funcs)
	for _, n := range []string{"layout", name} {
		src, from, err := w.file(n, ".txt", lang)
		if err == nil && n == "layout" {
			_, err = tpl.Parse(src)
		} else if err == nil {
			_, err = tpl.New(n).Funcs(funcs).Parse(src)
		}
		if err != nil {
			return "", "", w.failed(n+".txt", from, err)
		}
	}
	var subject, body bytes.Buffer
	if err := tpl.ExecuteTemplate(&subject, "subject", d); err != nil {
		return "", "", w.failed(name+".txt", "", err)
	}
	if err := tpl.ExecuteTemplate(&body, "layout", d); err != nil {
		return "", "", w.failed(name+".txt", "", err)
	}
	return strings.TrimSpace(subject.String()), strings.TrimSpace(body.String()) + "\n", nil
}

// html writes the HTML version.
func (w *Writer) html(name, lang string, t func(string) string, d data) (string, error) {
	funcs := htmltemplate.FuncMap{"t": t}
	tpl := htmltemplate.New("layout").Funcs(funcs)
	for _, n := range []string{"layout", name} {
		src, from, err := w.file(n, ".html", lang)
		if err == nil && n == "layout" {
			_, err = tpl.Parse(src)
		} else if err == nil {
			_, err = tpl.New(n).Funcs(funcs).Parse(src)
		}
		if err != nil {
			return "", w.failed(n+".html", from, err)
		}
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "layout", d); err != nil {
		return "", w.failed(name+".html", "", err)
	}
	return out.String(), nil
}

// failed says which template failed, so an operator can fix theirs.
func (w *Writer) failed(name, from string, err error) error {
	if from != "" {
		name = from + " (in " + w.Dir + ")"
	}
	return fmt.Errorf("emails: %s: %w", name, err)
}
