// Package cookie sets and reads Overfin's cookies, the same way everywhere.
//
// Over HTTPS, a cookie gets the __Host- prefix and the Secure flag, so no
// other subdomain can set it and it never travels unencrypted (SECURITY.md
// rule 17). Over plain http, which an operator may choose for a LAN or a
// test, browsers refuse both, so the cookie goes without them. A cookie set
// one way is never read the other way: a plain-http cookie can't stand in
// for an HTTPS one.
//
// Every cookie is HttpOnly, for the whole site (Path=/), and SameSite=Strict
// unless the caller asks for Lax.
package cookie

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// AlwaysSecure marks every request as HTTPS, for a site whose public
// address is https:// even if its proxy doesn't say so.
func AlwaysSecure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, true)))
	})
}

// Secure reports whether the request came over HTTPS: on a TLS connection,
// through a proxy that says so (X-Forwarded-Proto), or on a site marked
// AlwaysSecure. Anyone can claim the header; it only ever makes cookies
// stricter.
func Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if always, _ := r.Context().Value(ctxKey{}).(bool); always {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// Name is the cookie's name for this request: base, with the __Host- prefix
// over HTTPS.
func Name(r *http.Request, base string) string {
	if Secure(r) {
		return "__Host-" + base
	}
	return base
}

// Set sets c, whose Name is the base name, as this request's scheme allows.
func Set(w http.ResponseWriter, r *http.Request, c *http.Cookie) {
	out := *c
	out.Name = Name(r, c.Name)
	out.Path = "/"
	out.Domain = ""
	out.Secure = Secure(r)
	out.HttpOnly = true
	if out.SameSite != http.SameSiteLaxMode {
		out.SameSite = http.SameSiteStrictMode
	}
	http.SetCookie(w, &out)
}

// Get reads the cookie with this base name, as set over this request's
// scheme.
func Get(r *http.Request, base string) (*http.Cookie, error) {
	return r.Cookie(Name(r, base))
}

// Clear removes the cookie with this base name.
func Clear(w http.ResponseWriter, r *http.Request, base string) {
	Set(w, r, &http.Cookie{Name: base, Value: "", MaxAge: -1})
}
