// Package sdkcheck keeps the demo site on the current JavaScript SDK.
package sdkcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The demo site ships its own copy of the SDK, so the folder can be served
// as is. The copy must be the SDK itself, never a variant.
func TestDemoSiteUsesTheSDK(t *testing.T) {
	root := filepath.Join("..", "..")
	sdk, err := os.ReadFile(filepath.Join(root, "sdk", "js", "overfin.js"))
	if err != nil {
		t.Fatal(err)
	}
	demo, err := os.ReadFile(filepath.Join(root, "examples", "demo-site", "overfin.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sdk, demo) {
		t.Error("examples/demo-site/overfin.js differs from sdk/js/overfin.js; copy it over")
	}
}
