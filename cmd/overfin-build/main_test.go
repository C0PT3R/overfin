package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	src, err := generate([]string{"example.org/mod@v1.2.0", "github.com/someone/other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`m3 "example.org/mod"`, `m4 "github.com/someone/other"`, "m0.Module{}", "m4.Module{}", "DO NOT EDIT", "Site: web.Files"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
}

func TestGenerateRefuses(t *testing.T) {
	for _, bad := range []string{
		`example.org/mod"; os.Exit(1)`, "example.org/mod@v1; rm -rf /", "nodomain", "../escape/mod",
		"example.org/*", "-flag/mod", "example.org/mod@", "github.com/C0PT3R/overfin-gateway/modules/watchlist",
	} {
		if _, err := generate([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := generate([]string{"example.org/a", "example.org/a@v2"}); err == nil {
		t.Error("the same module twice accepted")
	}
}

// The official list here is the official build's.
func TestOfficialMatchesTheOfficialBuild(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "overfin", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	var imported []string
	for _, m := range regexp.MustCompile(`"(github\.com/C0PT3R/overfin-gateway/modules/[^"]+)"`).FindAllStringSubmatch(string(b), -1) {
		imported = append(imported, m[1])
	}
	if !slices.Equal(imported, official) {
		t.Errorf("cmd/overfin imports %v; overfin-build's official list is %v", imported, official)
	}
}
