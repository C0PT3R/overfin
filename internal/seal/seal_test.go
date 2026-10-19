package seal

import (
	"bytes"
	"testing"
)

func TestSealOpen(t *testing.T) {
	box, err := New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed := box.Seal([]byte("jellyfin-token"), "prof_a")

	if bytes.Contains(sealed, []byte("jellyfin-token")) {
		t.Fatal("plaintext visible in sealed value")
	}
	got, err := box.Open(sealed, "prof_a")
	if err != nil || string(got) != "jellyfin-token" {
		t.Fatalf("Open = %q, %v", got, err)
	}

	// Moved to another row: refused.
	if _, err := box.Open(sealed, "prof_b"); err != ErrOpen {
		t.Errorf("opened with another context: %v", err)
	}
	// One flipped bit: refused.
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := box.Open(tampered, "prof_a"); err != ErrOpen {
		t.Errorf("opened a tampered value: %v", err)
	}
	// Another key: refused.
	other, _ := New(bytes.Repeat([]byte{4}, 32))
	if _, err := other.Open(sealed, "prof_a"); err != ErrOpen {
		t.Errorf("opened with another key: %v", err)
	}
	// Same input twice: different output (random nonce).
	if bytes.Equal(sealed, box.Seal([]byte("jellyfin-token"), "prof_a")) {
		t.Error("sealing is deterministic")
	}
}
