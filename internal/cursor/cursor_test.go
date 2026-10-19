package cursor

import (
	"bytes"
	"strings"
	"testing"
)

var key = bytes.Repeat([]byte{1}, 32)

const binding = "GET /api/v1/items?kind=movie&sort=name"

func TestRoundTrip(t *testing.T) {
	c, err := Encode(key, binding, []byte("offset=50"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(key, binding, c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "offset=50" {
		t.Errorf("payload = %q", got)
	}
}

func TestRejects(t *testing.T) {
	good, _ := Encode(key, binding, []byte("offset=50"))

	// Flip one character in the middle of the cursor.
	b := []byte(good)
	if b[3] == 'A' {
		b[3] = 'B'
	} else {
		b[3] = 'A'
	}
	tampered := string(b)

	otherKey := bytes.Repeat([]byte{2}, 32)
	signedElsewhere, _ := Encode(otherKey, binding, []byte("offset=50"))

	tests := []struct {
		name, binding, cursor string
	}{
		{"tampered", binding, tampered},
		{"other query", "GET /api/v1/items?kind=series&sort=name", good},
		{"other key", binding, signedElsewhere},
		{"garbage", binding, "!!!not-base64!!!"},
		{"empty", binding, ""},
		{"too long", binding, strings.Repeat("A", 1000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(key, tt.binding, tt.cursor); err != ErrInvalid {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestPayloadLimit(t *testing.T) {
	if _, err := Encode(key, binding, make([]byte, MaxPayload+1)); err == nil {
		t.Error("oversized payload accepted")
	}
}
