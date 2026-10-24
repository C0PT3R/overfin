package cursor

import (
	"bytes"
	"testing"
)

// A cursor comes back as it went out, and anything else is refused,
// without a panic.
func FuzzCursor(f *testing.F) {
	f.Add([]byte("payload"), "binding", "x")
	f.Fuzz(func(t *testing.T, payload []byte, binding, junk string) {
		key := []byte("cursor key")
		c, err := Encode(key, binding, payload)
		if err == nil {
			got, err := Decode(key, binding, c)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("round trip: %q %v", got, err)
			}
			if _, err := Decode(key, binding+"x", c); err == nil {
				t.Fatal("another binding took it")
			}
		}
		Decode(key, binding, junk)
	})
}
