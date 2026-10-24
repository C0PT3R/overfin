package api

import (
	"testing"
	"time"
)

// Each account gets its searches a minute; a minute later, again.
func TestAccountLimiter(t *testing.T) {
	l := &AccountLimiter{PerMinute: 3}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.allow("a", now) {
			t.Fatalf("search %d refused", i+1)
		}
	}
	if l.allow("a", now) {
		t.Error("a fourth search allowed")
	}
	if !l.allow("b", now) {
		t.Error("another account refused")
	}
	if !l.allow("a", now.Add(61*time.Second)) {
		t.Error("refused a minute later")
	}
}
