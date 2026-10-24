// Package plugin defines how features plug into the Overfin gateway.
//
// A plugin never gets the raw HTTP mux. It gets a Registrar, which only
// accepts fixed routes under /api/v1/. Catch-all patterns are refused, so no
// plugin can turn the gateway into a passthrough proxy.
//
// Routes are protected by default: Handle wraps every route in the
// registrar's protection (the identity check). Only HandlePublic skips it,
// so forgetting a check isn't possible.
package plugin

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Plugin is one feature of the gateway, such as library browsing or playback.
type Plugin interface {
	// Name is a short, unique identifier, like "jellyfin-library".
	Name() string
	// Register declares every route the plugin serves.
	Register(r *Registrar) error
}

// Registrar collects the routes plugins declare.
type Registrar struct {
	mux     *http.ServeMux
	protect func(http.Handler) http.Handler
	owner   string
	routes  map[string]string // pattern -> plugin that registered it
	public  map[string]bool   // patterns registered with HandlePublic
	nav     map[string]bool   // paths registered with HandleNavigation
	codes   map[string]bool   // their prefixes, for those ending in a code
	cross   map[string]bool   // paths registered with HandleCrossSite
}

// NewRegistrar returns a Registrar that adds routes to mux, wrapping every
// non-public route with protect.
func NewRegistrar(mux *http.ServeMux, protect func(http.Handler) http.Handler) *Registrar {
	return &Registrar{mux: mux, protect: protect, routes: make(map[string]string),
		public: make(map[string]bool), nav: make(map[string]bool), codes: make(map[string]bool), cross: make(map[string]bool)}
}

// Load registers each plugin's routes, stopping at the first error.
func (r *Registrar) Load(plugins ...Plugin) error {
	for _, p := range plugins {
		r.owner = p.Name()
		if err := p.Register(r); err != nil {
			return fmt.Errorf("plugin %s: %w", p.Name(), err)
		}
	}
	r.owner = ""
	return nil
}

// Handle adds one protected route. The pattern must be "METHOD /api/v1/...",
// using Go's ServeMux syntax, for example "GET /api/v1/items/{id}".
func (r *Registrar) Handle(pattern string, h http.Handler) error {
	return r.add(pattern, r.protect(h), false)
}

// HandlePublic adds a route that needs no identity. Public routes must never
// reveal anything about users, upstream servers or software versions
// (SECURITY.md rule 37).
func (r *Registrar) HandlePublic(pattern string, h http.Handler) error {
	return r.add(pattern, h, true)
}

// HandleNavigation adds a public GET route that another site may send the
// browser to, such as an identity provider returning from a login, or an
// email's link. Every other route refuses cross-site requests. The path
// must be fixed, but may end in one code ("/api/v1/invite/{code}"), and
// the handler must not act on anything but that code and its own query.
func (r *Registrar) HandleNavigation(pattern string, h http.Handler) error {
	method, path, _ := strings.Cut(pattern, " ")
	prefix, last := path, ""
	if i := strings.LastIndex(path, "/"); i >= 0 {
		prefix, last = path[:i+1], path[i+1:]
	}
	coded := strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}") && !strings.Contains(last, "...")
	if method != http.MethodGet || strings.Contains(prefix, "{") || (strings.Contains(last, "{") && !coded) {
		return fmt.Errorf("route %q: navigation routes must be GET with a fixed path, ending in a code at most", pattern)
	}
	if err := r.add(pattern, h, true); err != nil {
		return err
	}
	if coded {
		r.codes[prefix] = true
	} else {
		r.nav[path] = true
	}
	return nil
}

// HandlePage adds one of Overfin's own pages at the site's root, such as
// "GET /signin" or "GET /invite/{code}": public, GET only, one plain word
// and at most a code after it, served before the site's own pages. People
// arrive there from elsewhere, so it's a navigation route.
func (r *Registrar) HandlePage(pattern string, h http.Handler) error {
	method, path, _ := strings.Cut(pattern, " ")
	if method != http.MethodGet || !pagePattern.MatchString(path) {
		return fmt.Errorf("route %q: pages are GET, one word at the root, and a code at most", pattern)
	}
	if prev, ok := r.routes[pattern]; ok {
		return fmt.Errorf("route %q already registered by %s", pattern, prev)
	}
	r.routes[pattern] = r.owner
	r.public[pattern] = true
	r.mux.Handle(pattern, h)
	if prefix, coded := strings.CutSuffix(path, "/{code}"); coded {
		r.codes[prefix+"/"] = true
	} else {
		r.nav[path] = true
	}
	return nil
}

// pagePattern is a page's path: /word, or /word/{code}.
var pagePattern = regexp.MustCompile(`^/[a-z]{2,16}(/\{code\})?$`)

// HandleCrossSite adds a public GET route that accepts any request, from
// any site: the gate a reverse proxy consults before serving the site's
// pages. It must answer only whether the visitor is logged in.
func (r *Registrar) HandleCrossSite(pattern string, h http.Handler) error {
	method, path, _ := strings.Cut(pattern, " ")
	if method != http.MethodGet || strings.Contains(path, "{") {
		return fmt.Errorf("route %q: cross-site routes must be GET with a fixed path", pattern)
	}
	if err := r.add(pattern, h, true); err != nil {
		return err
	}
	r.cross[path] = true
	return nil
}

// IsCrossSite reports whether a request path belongs to a cross-site route.
func (r *Registrar) IsCrossSite(path string) bool {
	return r.cross[path]
}

// IsNavigation reports whether a request path belongs to a navigation route.
func (r *Registrar) IsNavigation(path string) bool {
	if r.nav[path] {
		return true
	}
	i := strings.LastIndex(path, "/")
	return i >= 0 && i < len(path)-1 && r.codes[path[:i+1]]
}

func (r *Registrar) add(pattern string, h http.Handler, public bool) error {
	if err := checkPattern(pattern); err != nil {
		return err
	}
	if prev, ok := r.routes[pattern]; ok {
		return fmt.Errorf("route %q already registered by %s", pattern, prev)
	}
	r.routes[pattern] = r.owner
	r.public[pattern] = public
	r.mux.Handle(pattern, h)
	return nil
}

// IsPublic reports whether pattern was registered with HandlePublic.
func (r *Registrar) IsPublic(pattern string) bool {
	return r.public[pattern]
}

// HandleFunc is Handle for plain functions.
func (r *Registrar) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) error {
	return r.Handle(pattern, http.HandlerFunc(f))
}

// Routes returns every registered pattern and the plugin that owns it.
func (r *Registrar) Routes() map[string]string {
	out := make(map[string]string, len(r.routes))
	for k, v := range r.routes {
		out[k] = v
	}
	return out
}

var allowedMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

func checkPattern(pattern string) error {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || !allowedMethods[method] {
		return fmt.Errorf("route %q: must start with GET, POST, PUT, PATCH or DELETE", pattern)
	}
	if !strings.HasPrefix(path, "/api/v1/") {
		return fmt.Errorf("route %q: path must start with /api/v1/", pattern)
	}
	if strings.HasSuffix(path, "/") {
		// In ServeMux, a trailing slash matches every path below it.
		return fmt.Errorf("route %q: trailing slash would match a whole subtree", pattern)
	}
	if strings.Contains(path, "...}") {
		// "{rest...}" captures the rest of the path: a passthrough in disguise.
		return fmt.Errorf("route %q: catch-all wildcards are not allowed", pattern)
	}
	return nil
}
