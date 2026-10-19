// Package cursor makes pagination cursors that clients can't tamper with
// (SECURITY.md rule 38).
//
// A cursor carries a small payload (an offset, a seed, an upstream page
// token) plus a signature. The signature also covers the query the cursor
// belongs to, so a cursor can't be modified, and can't be replayed with a
// different query either. The payload is signed, not encrypted: it holds
// nothing secret.
package cursor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// macSize is how many bytes of the HMAC are kept. 16 bytes (128 bits) is far
// beyond guessing range and keeps cursors short.
const macSize = 16

// MaxPayload bounds what a cursor may carry.
const MaxPayload = 256

// ErrInvalid is returned for any cursor that doesn't verify. Callers answer
// with the invalid-cursor problem, without saying why.
var ErrInvalid = errors.New("invalid cursor")

var enc = base64.RawURLEncoding

// Encode signs payload for the given query and returns the cursor.
// binding must describe the query canonically (same filters, same order),
// without the cursor itself.
func Encode(key []byte, binding string, payload []byte) (string, error) {
	if len(payload) > MaxPayload {
		return "", errors.New("cursor: payload too large")
	}
	out := append([]byte{}, payload...)
	out = append(out, mac(key, binding, payload)...)
	return enc.EncodeToString(out), nil
}

// Decode verifies c against the query binding and returns its payload.
func Decode(key []byte, binding string, c string) ([]byte, error) {
	raw, err := enc.DecodeString(c)
	if err != nil || len(raw) < macSize || len(raw) > MaxPayload+macSize {
		return nil, ErrInvalid
	}
	payload, got := raw[:len(raw)-macSize], raw[len(raw)-macSize:]
	// hmac.Equal compares in constant time, so timing reveals nothing about
	// how close a forged signature came.
	if !hmac.Equal(got, mac(key, binding, payload)) {
		return nil, ErrInvalid
	}
	return payload, nil
}

func mac(key []byte, binding string, payload []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(binding))
	// A separator byte, so binding "ab"+payload "c" can't collide with
	// binding "a"+payload "bc".
	h.Write([]byte{0})
	h.Write(payload)
	return h.Sum(nil)[:macSize]
}
