// Package sdkcheck keeps overfin-web on the current JavaScript SDK.
package sdkcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// overfin-web ships its own copy of the SDK, so its files are served as they
// are. The copy must be the SDK itself, never a variant.
func TestWebUsesTheSDK(t *testing.T) {
	root := filepath.Join("..", "..")
	sdk, err := os.ReadFile(filepath.Join(root, "sdk", "js", "overfin.js"))
	if err != nil {
		t.Fatal(err)
	}
	web, err := os.ReadFile(filepath.Join(root, "web", "overfin.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sdk, web) {
		t.Error("web/overfin.js differs from sdk/js/overfin.js; copy it over")
	}
}
