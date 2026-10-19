// Package sourcecheck holds repository-wide checks on the source itself.
package sourcecheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// invisible are characters that can make source code read differently from
// what it does ("Trojan Source"): direction overrides and isolates, and byte
// order marks inside a file. Escape them ("\u202E") instead.
func invisible(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069,
		r == 0x200E, r == 0x200F, r == 0x061C, r == 0xFEFF:
		return true
	}
	return false
}

func TestNoInvisibleCharactersInSource(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".sql") ||
			strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".toml")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, r := range line {
				if invisible(r) {
					t.Errorf("%s:%d: invisible character U+%04X", path, i+1, r)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
