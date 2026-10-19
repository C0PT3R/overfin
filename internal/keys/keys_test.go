package keys

import (
	"bytes"
	"testing"
)

func TestDeriveIsStableAndSeparate(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	a, err := Derive(secret)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Derive(secret)
	if err != nil {
		t.Fatal(err)
	}
	// Same secret, same keys: restarts must not invalidate cursors or tokens.
	if !bytes.Equal(a.Cursor, b.Cursor) || !bytes.Equal(a.TokenEncryption, b.TokenEncryption) {
		t.Error("derivation is not stable")
	}
	// Every purpose gets a different key.
	all := [][]byte{a.Cursor, a.LogID, a.TokenEncryption, a.PIN}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if bytes.Equal(all[i], all[j]) {
				t.Errorf("keys %d and %d are equal", i, j)
			}
		}
	}
	// A different secret gives different keys.
	c, _ := Derive(bytes.Repeat([]byte{8}, 32))
	if bytes.Equal(a.Cursor, c.Cursor) {
		t.Error("different secrets produced the same key")
	}
}
