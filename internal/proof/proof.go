// Package proof is the sign-in pages' check against scripts: before a
// form is sent, the browser must find a number that, after the server's
// challenge, gives a SHA-256 starting with Bits zero bits. It takes a
// second or two in a browser, a microsecond to check, and repeated in
// bulk it costs a script real computing time. Nothing leaves the server:
// no third party is asked.
package proof

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"math/bits"
	"strings"
	"sync"
	"time"
)

// Bits is how many zero bits an answer's hash starts with: about 130,000
// tries on average.
const Bits = 17

// lifetime is how long a challenge works.
const lifetime = 10 * time.Minute

// Checker makes challenges and checks answers, each once.
type Checker struct {
	// Key signs challenges, so they can't be made up.
	Key []byte

	mu   sync.Mutex
	used map[string]time.Time
}

// Challenge is a new challenge: random, signed, expiring.
func (c *Checker) Challenge() string {
	b := make([]byte, 24)
	rand.Read(b[:16])
	binary.BigEndian.PutUint64(b[16:], uint64(time.Now().Add(lifetime).Unix()))
	body := base64.RawURLEncoding.EncodeToString(b)
	return body + "." + c.sign(body)
}

func (c *Checker) sign(body string) string {
	m := hmac.New(sha256.New, c.Key)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// Check reports whether answer solves challenge, a challenge made here and
// still working, never solved before.
func (c *Checker) Check(challenge, answer string) bool {
	body, sig, ok := strings.Cut(challenge, ".")
	if !ok || len(answer) == 0 || len(answer) > 16 || !hmac.Equal([]byte(sig), []byte(c.sign(body))) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(b) != 24 {
		return false
	}
	expires := time.Unix(int64(binary.BigEndian.Uint64(b[16:])), 0)
	now := time.Now()
	if now.After(expires) || !Solves(challenge, answer) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used == nil {
		c.used = map[string]time.Time{}
	}
	for k, at := range c.used {
		if now.After(at) {
			delete(c.used, k)
		}
	}
	if _, seen := c.used[body]; seen {
		return false
	}
	c.used[body] = expires
	return true
}

// Solves reports whether SHA-256(challenge ":" answer) starts with Bits
// zero bits.
func Solves(challenge, answer string) bool {
	sum := sha256.Sum256([]byte(challenge + ":" + answer))
	return bits.LeadingZeros32(binary.BigEndian.Uint32(sum[:4])) >= Bits
}
