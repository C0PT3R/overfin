// Package seal encrypts small secrets, such as upstream tokens, before they
// are stored (SECURITY.md rule 20).
//
// It uses AES-256-GCM, which also detects any change to the ciphertext. Each
// sealed value is bound to a context string (the ID of the row it belongs
// to): a value copied into another row fails to open, so even someone who can
// write to the database can't swap one user's token into another's profile.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// version is the first byte of every sealed value, so the format can change
// later without guessing.
const version = 1

// ErrOpen is returned for any value that fails to decrypt or verify.
var ErrOpen = errors.New("seal: cannot open value")

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("seal: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext, bound to context.
func (b *Box) Seal(plaintext []byte, context string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce) // a fresh random nonce for every value
	out := append([]byte{version}, nonce...)
	return b.aead.Seal(out, nonce, plaintext, []byte(context))
}

// Open decrypts a value sealed with the same key and context.
func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < 1+n+b.aead.Overhead() || sealed[0] != version {
		return nil, ErrOpen
	}
	nonce, ciphertext := sealed[1:1+n], sealed[1+n:]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(context))
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}
