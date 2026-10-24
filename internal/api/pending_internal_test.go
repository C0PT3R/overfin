package api

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

// One client asking for logins over and over pushes out its own oldest,
// not anyone else's.
func TestPendingPerClient(t *testing.T) {
	a := &Auth{pending: map[string]pendingAuth{}}
	start := time.Now()
	a.pending["b"] = pendingAuth{From: "198.51.100.2", Started: start}
	for i := range maxPendingFrom {
		a.pending[fmt.Sprint("a", i)] = pendingAuth{From: "198.51.100.1", Started: start.Add(time.Duration(i+1) * time.Second)}
	}
	a.evictOldest(func(v pendingAuth) bool { return v.From == "198.51.100.1" }, maxPendingFrom)
	if _, ok := a.pending["a0"]; ok {
		t.Error("the client's oldest stayed")
	}
	if _, ok := a.pending["b"]; !ok {
		t.Error("another client's went")
	}
	if a.evictOldest(func(pendingAuth) bool { return true }, maxPending); len(a.pending) != maxPendingFrom {
		t.Errorf("under the total cap, %d left", len(a.pending))
	}

	// An IPv6 connection's whole /64 is one client.
	x, y := netip.MustParseAddr("2001:db8:1:2::1"), netip.MustParseAddr("2001:db8:1:2:ffff::9")
	if clientFrom(x) != clientFrom(y) || clientFrom(x) == clientFrom(netip.MustParseAddr("2001:db8:1:3::1")) {
		t.Errorf("IPv6: %s %s", clientFrom(x), clientFrom(y))
	}
	if clientFrom(netip.MustParseAddr("::ffff:198.51.100.1")) != "198.51.100.1" {
		t.Error("IPv4 in IPv6")
	}
}
