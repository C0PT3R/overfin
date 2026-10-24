// Package keys derives Overfin's working keys from the one secret key.
//
// Each purpose gets its own key, derived with HKDF (RFC 5869). Knowing one
// derived key reveals nothing about the others or about the secret key, so a
// mistake in one use can't spread to another.
package keys

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

// Keys holds one key per purpose.
type Keys struct {
	Cursor          []byte // signs pagination cursors
	LogID           []byte // keyed hash of user IDs in logs
	TokenEncryption []byte // encrypts upstream tokens at rest
	PIN             []byte // keyed hash of profile PINs
	EmailLink       []byte // keyed hash of the links sent by email
	Proof           []byte // signs the pages' checks against scripts
	// Check identifies the secret key without revealing it: stored in the
	// database, it tells a wrong key at startup.
	Check []byte
}

const size = 32

// Labels are part of the derivation. Changing one changes its key, so they
// carry a version.
const (
	labelCursor = "overfin cursor v1"
	labelLogID  = "overfin log-id v1"
	labelToken  = "overfin token-encryption v1"
	labelPIN    = "overfin pin v1"
	labelLink   = "overfin signin-code v1" // its first use kept its name
	labelProof  = "overfin proof v1"
	labelCheck  = "overfin key-check v1"
)

// Derive returns the working keys for the given secret key.
func Derive(secret []byte) (*Keys, error) {
	derive := func(label string) ([]byte, error) {
		k, err := hkdf.Key(sha256.New, secret, nil, label, size)
		if err != nil {
			return nil, fmt.Errorf("keys: deriving %q: %w", label, err)
		}
		return k, nil
	}
	var k Keys
	var err error
	if k.Cursor, err = derive(labelCursor); err != nil {
		return nil, err
	}
	if k.LogID, err = derive(labelLogID); err != nil {
		return nil, err
	}
	if k.TokenEncryption, err = derive(labelToken); err != nil {
		return nil, err
	}
	if k.PIN, err = derive(labelPIN); err != nil {
		return nil, err
	}
	if k.EmailLink, err = derive(labelLink); err != nil {
		return nil, err
	}
	if k.Proof, err = derive(labelProof); err != nil {
		return nil, err
	}
	if k.Check, err = derive(labelCheck); err != nil {
		return nil, err
	}
	return &k, nil
}
