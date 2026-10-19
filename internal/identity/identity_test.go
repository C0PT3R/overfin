package identity

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

var proxy = []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")}

const (
	trustedPeer   = "198.51.100.7:40000"
	untrustedPeer = "203.0.113.50:40000"
)

func TestForwardAuth(t *testing.T) {
	res := NewForwardAuth(proxy, "Remote-User", "Remote-Groups", "|")

	tests := []struct {
		name        string
		peer        string
		headers     map[string]string
		wantOK      bool
		wantSubject string
		wantGroups  int
	}{
		{"trusted proxy with user", trustedPeer,
			map[string]string{"Remote-User": "abc123", "Remote-Groups": "media|family"},
			true, "abc123", 2},
		{"groups with spaces and accents", trustedPeer,
			map[string]string{"Remote-User": "abc123", "Remote-Groups": "authentik Admins|Utilisateurs médias"},
			true, "abc123", 2},
		{"a comma is part of a name, not a separator", trustedPeer,
			map[string]string{"Remote-User": "abc123", "Remote-Groups": "staff,media-users"},
			true, "abc123", 1},
		{"an unusable group is dropped, not the identity", trustedPeer,
			map[string]string{"Remote-User": "abc123", "Remote-Groups": "media|bad\x07name"},
			true, "abc123", 1},
		{"trusted proxy without user", trustedPeer, nil, false, "", 0},
		{"untrusted peer with user header", untrustedPeer,
			map[string]string{"Remote-User": "admin"}, false, "", 0},
		{"control character in user", trustedPeer,
			map[string]string{"Remote-User": "abc\x00def"}, false, "", 0},
		{"space in user", trustedPeer,
			map[string]string{"Remote-User": "abc def"}, false, "", 0},
		{"user too long", trustedPeer,
			map[string]string{"Remote-User": strings.Repeat("a", maxSubjectLen+1)}, false, "", 0},
		{"too many groups", trustedPeer,
			map[string]string{"Remote-User": "abc", "Remote-Groups": strings.Repeat("g|", maxGroups+1)},
			false, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/me", nil)
			req.RemoteAddr = tt.peer
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			id, ok, _ := res.Resolve(req)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if id.Subject != tt.wantSubject || len(id.Groups) != tt.wantGroups {
				t.Errorf("got subject %q with %d groups", id.Subject, len(id.Groups))
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	res := NewForwardAuth(proxy, "Remote-User", "", "|")

	tests := []struct {
		name, peer, xff, want string
	}{
		{"trusted proxy: last entry wins", trustedPeer, "192.0.2.1, 192.0.2.99", "192.0.2.99"},
		{"trusted proxy: single entry", trustedPeer, "192.0.2.99", "192.0.2.99"},
		{"trusted proxy: garbage falls back to peer", trustedPeer, "not-an-ip", "198.51.100.7"},
		{"untrusted peer: header ignored", untrustedPeer, "192.0.2.99", "203.0.113.50"},
		{"no header", untrustedPeer, "", "203.0.113.50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.peer
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			_, _, client := res.Resolve(req)
			if client.String() != tt.want {
				t.Errorf("client = %s, want %s", client, tt.want)
			}
		})
	}
}

func TestAnonymousIgnoresIdentityHeaders(t *testing.T) {
	res := NewAnonymous(proxy)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = trustedPeer
	req.Header.Set("Remote-User", "admin")

	id, ok, _ := res.Resolve(req)
	if !ok || !id.Anonymous || id.Subject != "" {
		t.Errorf("got %+v, ok=%v; want anonymous with no subject", id, ok)
	}
}

func TestIPv4MappedPeer(t *testing.T) {
	res := NewForwardAuth(proxy, "Remote-User", "", "|")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::ffff:198.51.100.7]:40000"
	req.Header.Set("Remote-User", "abc")
	if _, ok, _ := res.Resolve(req); !ok {
		t.Error("an IPv4-mapped trusted peer was not recognized")
	}
}

// A client-sent header next to the proxy's must not win, so two values mean
// no identity at all.
func TestDuplicateIdentityHeaders(t *testing.T) {
	res := NewForwardAuth(proxy, "Remote-User", "Remote-Groups", "|")

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = trustedPeer
	req.Header.Add("Remote-User", "admin")
	req.Header.Add("Remote-User", "abc123")
	if _, ok, _ := res.Resolve(req); ok {
		t.Error("two user headers were accepted")
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = trustedPeer
	req.Header.Set("Remote-User", "abc123")
	req.Header.Add("Remote-Groups", "admins")
	req.Header.Add("Remote-Groups", "media")
	if _, ok, _ := res.Resolve(req); ok {
		t.Error("two groups headers were accepted")
	}
}

func TestCommaSeparator(t *testing.T) {
	res := NewForwardAuth(proxy, "Remote-User", "Remote-Groups", ",")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = trustedPeer
	req.Header.Set("Remote-User", "abc")
	req.Header.Set("Remote-Groups", "media, family")
	id, ok, _ := res.Resolve(req)
	if !ok || len(id.Groups) != 2 || id.Groups[1] != "family" {
		t.Errorf("got %+v, ok=%v", id, ok)
	}
}
