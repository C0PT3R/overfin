// Package ids makes Overfin's resource IDs: a type prefix and 128 random bits
// (SECURITY.md rule 23), such as "prof_3kq9x2m8w7v1…".
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// Prefixes of the ID types Overfin issues.
const (
	Account  = "acct"
	Profile  = "prof"
	Session  = "sess"
	Login    = "login"
	Template = "tmpl"
)

// Lowercase base32 without padding: URL-safe and easy to read aloud.
var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// encodedLen is the length of 16 random bytes once encoded.
var encodedLen = enc.EncodedLen(16)

// New returns a fresh ID with the given prefix.
func New(prefix string) string {
	b := make([]byte, 16)
	rand.Read(b) // crypto/rand.Read never fails on supported systems
	return prefix + "_" + enc.EncodeToString(b)
}

// Valid reports whether s is a well-formed ID of the given type. It says
// nothing about whether the ID exists.
func Valid(prefix, s string) bool {
	rest, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(rest) != encodedLen {
		return false
	}
	_, err := enc.DecodeString(rest)
	return err == nil
}
