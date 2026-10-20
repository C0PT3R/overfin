// Package sdkcheck keeps overfin-web, as the official build bundles it, on
// the current JavaScript SDK.
package sdkcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	web "github.com/C0PT3R/overfin-web"
)

// overfin-web ships its own copy of the SDK. The version the official build
// bundles must carry this SDK itself, never a variant: after changing the
// SDK, update overfin-web and the version go.mod requires.
func TestWebUsesTheSDK(t *testing.T) {
	sdk, err := os.ReadFile(filepath.Join("..", "..", "sdk", "js", "overfin.js"))
	if err != nil {
		t.Fatal(err)
	}
	bundled, err := web.Files.ReadFile("overfin.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sdk, bundled) {
		t.Error("the overfin-web version in go.mod carries another SDK than sdk/js/overfin.js; update overfin-web")
	}
}
