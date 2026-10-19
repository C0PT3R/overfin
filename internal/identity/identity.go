// Package identity works out who is making each request (SECURITY.md rules
// 13 and 14).
//
// In forward-auth mode, a reverse proxy authenticates users and passes their
// identity in headers. Those headers are believed only when the connection
// comes from a trusted proxy; from anyone else they are ignored, exactly as
// if they were absent. The same goes for X-Forwarded-For.
package identity

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Identity is who the visitor is.
type Identity struct {
	// Subject is the stable user ID from the identity provider. Empty for
	// anonymous visitors.
	Subject string
	// Groups the user belongs to, as reported by the proxy.
	Groups []string
	// Anonymous is true in anonymous mode.
	Anonymous bool
}

// Limits on what the proxy may send. Real values are far smaller; anything
// beyond these is treated as no identity at all.
const (
	maxSubjectLen = 256
	maxGroups     = 100
	maxGroupLen   = 128
)

// Resolver turns a request into an identity and a client IP.
type Resolver struct {
	mode         string // "forwardauth", "anonymous" or "none"
	proxies      []netip.Prefix
	userHeader   string
	groupsHeader string
	groupsSep    string
}

// NewForwardAuth trusts identity headers from the given proxies only. sep
// separates groups in the groups header: "|" for Authentik, "," for
// Authelia.
func NewForwardAuth(proxies []netip.Prefix, userHeader, groupsHeader, sep string) *Resolver {
	if sep == "" {
		sep = "|"
	}
	return &Resolver{mode: "forwardauth", proxies: proxies, userHeader: userHeader, groupsHeader: groupsHeader, groupsSep: sep}
}

// NewAnonymous gives every visitor the anonymous identity. Identity headers
// are ignored, but X-Forwarded-For from trusted proxies is still used for the
// client IP.
// NewHeaderless returns a Resolver that takes no identity from the request
// at all: with Overfin's own login, identity comes from the login cookie,
// and identity headers are ignored whoever sends them. The proxies are
// still trusted for the client address.
func NewHeaderless(proxies []netip.Prefix) *Resolver {
	return &Resolver{mode: "none", proxies: proxies}
}

func NewAnonymous(proxies []netip.Prefix) *Resolver {
	return &Resolver{mode: "anonymous", proxies: proxies}
}

// Resolve returns the request's identity (ok is false when there is none) and
// the client's IP address.
func (r *Resolver) Resolve(req *http.Request) (id Identity, ok bool, client netip.Addr) {
	peer := peerAddr(req)
	trusted := r.trusted(peer)

	client = peer
	if trusted {
		if fwd, found := lastForwardedFor(req.Header); found {
			client = fwd
		}
	}

	if r.mode == "anonymous" {
		return Identity{Anonymous: true}, true, client
	}
	if !trusted || r.mode == "none" {
		return Identity{}, false, client
	}

	subject, single := only(req.Header, r.userHeader)
	if !single || !validValue(subject, maxSubjectLen) {
		return Identity{}, false, client
	}
	groupsValue := ""
	if r.groupsHeader != "" {
		if groupsValue, single = only(req.Header, r.groupsHeader); !single {
			return Identity{}, false, client
		}
	}
	groups, valid := parseGroups(groupsValue, r.groupsSep)
	if !valid {
		return Identity{}, false, client
	}
	return Identity{Subject: subject, Groups: groups}, true, client
}

// only returns the header's value, with single false when the header appears
// more than once. Two values mean a client-sent header slipped past the
// proxy, and which one is real can't be known, so neither is believed.
func only(h http.Header, name string) (value string, single bool) {
	values := h.Values(name)
	switch len(values) {
	case 0:
		return "", true
	case 1:
		return values[0], true
	default:
		return "", false
	}
}

func (r *Resolver) trusted(peer netip.Addr) bool {
	if !peer.IsValid() {
		return false
	}
	for _, p := range r.proxies {
		if p.Contains(peer) {
			return true
		}
	}
	return false
}

// peerAddr is the address of whoever opened the connection.
func peerAddr(req *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(req.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	// An IPv4 address seen through an IPv6 socket looks like ::ffff:1.2.3.4.
	return ap.Addr().Unmap()
}

// lastForwardedFor returns the last address in X-Forwarded-For: the one the
// trusted proxy itself appended. Earlier entries come from the client and
// can't be believed.
func lastForwardedFor(h http.Header) (netip.Addr, bool) {
	values := h.Values("X-Forwarded-For")
	if len(values) == 0 {
		return netip.Addr{}, false
	}
	parts := strings.Split(values[len(values)-1], ",")
	addr, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// parseGroups splits a groups header on the configured separator only. Group
// names may contain spaces and accents ("authentik Admins", "Utilisateurs
// médias"). An unusable name is dropped rather than voiding the identity:
// dropping a group can only remove access, never add it. Too many groups, or
// an oversized header, still make the identity invalid.
func parseGroups(header, sep string) ([]string, bool) {
	if header == "" {
		return nil, true
	}
	if len(header) > maxGroups*(maxGroupLen+1) {
		return nil, false
	}
	fields := strings.Split(header, sep)
	if len(fields) > maxGroups {
		return nil, false
	}
	groups := make([]string, 0, len(fields))
	for _, g := range fields {
		g = strings.TrimSpace(g)
		if validGroup(g) {
			groups = append(groups, g)
		}
	}
	return groups, true
}

// validGroup accepts printable text up to maxGroupLen bytes: letters in any
// language, digits, spaces and punctuation, but no control characters and no
// invalid UTF-8.
func validGroup(g string) bool {
	if g == "" || len(g) > maxGroupLen || !utf8.ValidString(g) {
		return false
	}
	for _, r := range g {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// validValue accepts non-empty printable ASCII up to max bytes. Identity
// values end up in logs and databases; nothing exotic is allowed in.
func validValue(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// ---- Request context --------------------------------------------------------

// ctxKey is unexported, so no other package can read or overwrite the values.
type ctxKey int

const (
	identityKey ctxKey = iota
	clientKey
)

// FromContext returns the identity attached by Middleware.
// With returns ctx carrying id, for identity sources other than headers.
func With(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// CleanGroups keeps the valid group names among names, at most maxGroups.
func CleanGroups(names []string) []string {
	out := []string{}
	for _, g := range names {
		if g = strings.TrimSpace(g); validGroup(g) && len(out) < maxGroups {
			out = append(out, g)
		}
	}
	return out
}

// ValidSubject reports whether s can be a user ID.
func ValidSubject(s string) bool { return validValue(s, maxSubjectLen) }

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}

// ClientIP returns the client address attached by Middleware.
func ClientIP(ctx context.Context) netip.Addr {
	addr, _ := ctx.Value(clientKey).(netip.Addr)
	return addr
}

// Middleware resolves every request once and attaches the result to its
// context. It refuses nothing by itself; Require does that.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id, ok, client := r.Resolve(req)
		ctx := context.WithValue(req.Context(), clientKey, client)
		if ok {
			ctx = context.WithValue(ctx, identityKey, id)
		}
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}
