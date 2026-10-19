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
}

// NewRegistrar returns a Registrar that adds routes to mux, wrapping every
// non-public route with protect.
func NewRegistrar(mux *http.ServeMux, protect func(http.Handler) http.Handler) *Registrar {
	return &Registrar{mux: mux, protect: protect, routes: make(map[string]string), public: make(map[string]bool)}
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
