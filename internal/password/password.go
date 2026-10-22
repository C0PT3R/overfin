// Package password hashes the passwords people sign in with, when they set
// one: PBKDF2 with SHA-256 (standard library), at the strength OWASP
// recommends, with a random salt each. A hash names its method and
// strength, so they can change later without breaking the stored ones.
package password

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	method     = "pbkdf2-sha256"
	iterations = 600_000
	saltSize   = 16
	keySize    = 32
)

// Lengths a password may have, in characters.
const (
	MinLength = 10
	MaxLength = 256
)

// ErrWeak: the password is too short or too long.
var ErrWeak = fmt.Errorf("password: a password is %d to %d characters", MinLength, MaxLength)

// Hash returns the stored form of a password.
func Hash(pw string) (string, error) {
	if n := utf8.RuneCountInString(pw); n < MinLength || n > MaxLength {
		return "", ErrWeak
	}
	salt := make([]byte, saltSize)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pw, salt, iterations, keySize)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return method + "$" + strconv.Itoa(iterations) + "$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(key), nil
}

// Check reports whether pw matches a stored hash. A malformed hash matches
// nothing.
func Check(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != method || len(pw) > MaxLength*4 {
		return false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 100_000 || n > 10_000_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, n, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Dummy is a valid hash of no one's password: checking against it when an
// email has no password takes as long as a real check, so the time taken
// tells nothing.
var Dummy = func() string {
	h, err := Hash("not anyone's password")
	if err != nil {
		panic(errors.New("password: making the dummy hash"))
	}
	return h
}()
