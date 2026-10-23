// Package server builds the gateway's HTTP handler: the plugin routes plus
// the protections every response gets.
package server

import (
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
)

// maxBodyBytes caps request bodies. The API takes small JSON bodies only.
const maxBodyBytes = 64 << 10 // 64 KiB

// New returns the gateway handler with the given plugins loaded. identify
// puts the caller's identity in the request context; every route except the
// public ones requires one. site, when not nil, serves everything outside
// /api/: the website.
func New(logger *slog.Logger, identify func(http.Handler) http.Handler, site http.Handler, plugins ...plugin.Plugin) (http.Handler, error) {
	mux := http.NewServeMux()
	reg := plugin.NewRegistrar(mux, requireIdentity)

	if err := reg.HandlePublic("GET /api/v1/health", http.HandlerFunc(health)); err != nil {
		return nil, err
	}
	if err := reg.Load(plugins...); err != nil {
		return nil, err
	}
	// A batch's requests run through the same routes, for the identity
	// the batch itself was checked for.
	if err := reg.Handle("POST /api/v1/batch", batch(mux)); err != nil {
		return nil, err
	}
	for pattern, owner := range reg.Routes() {
		logger.Info("route", "pattern", pattern, "plugin", owner, "public", reg.IsPublic(pattern))
	}

	// Anything no route matched gets the same plain 404, in the API; the
	// rest is the site's, when there is one.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if site != nil && !isAPI(r.URL.Path) {
			site.ServeHTTP(w, r)
			return
		}
		problem.Send(w, problem.NotFound, "")
	})

	return recoverPanics(logger, securityHeaders(sameOrigin(reg, limitBody(identify(mux))))), nil
}

// requireIdentity refuses requests that carry no identity (SECURITY.md rule
// 13). The registrar applies it to every route that isn't public.
func requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := identity.FromContext(r.Context()); !ok {
			problem.Send(w, problem.NotAuthenticated, "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAPI reports whether a path is the API's, never the site's.
func isAPI(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

func health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// securityHeaders sets headers that lock down how browsers treat responses.
// The gateway only serves JSON and media, so the policy can be very strict.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin enforces SECURITY.md rule 18. Browsers label every request with
// Sec-Fetch-Site; only same-origin requests (and "none", for an address typed
// directly) are accepted, which also shuts out sibling subdomains. Requests
// that change something must be JSON, which plain HTML forms can't send: that
// covers old browsers without Sec-Fetch-Site. The only exception is a
// top-level GET navigation to a navigation route, and a GET to a
// cross-site route (the gate).
func sameOrigin(reg *plugin.Registrar, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
		default:
			if r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Mode") == "navigate" &&
				r.Header.Get("Sec-Fetch-Dest") == "document" && reg.IsNavigation(r.URL.Path) {
				break
			}
			if r.Method == http.MethodGet && reg.IsCrossSite(r.URL.Path) {
				break
			}
			// The site's files change nothing, and people arrive at its
			// pages from elsewhere (a link in an email).
			if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !isAPI(r.URL.Path) {
				break
			}
			problem.Send(w, problem.CrossSiteRequest, "")
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				problem.Send(w, problem.UnsupportedMediaType, "")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// recoverPanics turns a crash in one handler into a generic 500, so no stack
// trace or internal detail reaches the client.
func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v) // the client went away; let net/http handle it
				}
				// Log the route pattern, not the path: paths can carry
				// stream tickets, which must never reach the logs.
				logger.Error("panic", "value", v, "route", r.Pattern)
				problem.Send(w, problem.Internal, "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
