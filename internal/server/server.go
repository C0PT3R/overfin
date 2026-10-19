// Package server builds the gateway's HTTP handler: the plugin routes plus
// the protections every response gets.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/C0PT3R/overfin/internal/plugin"
)

// maxBodyBytes caps request bodies. The API takes small JSON bodies only.
const maxBodyBytes = 64 << 10 // 64 KiB

// New returns the gateway handler with the given plugins loaded.
func New(logger *slog.Logger, plugins ...plugin.Plugin) (http.Handler, error) {
	mux := http.NewServeMux()
	reg := plugin.NewRegistrar(mux)

	if err := reg.HandleFunc("GET /api/v1/health", health); err != nil {
		return nil, err
	}
	if err := reg.Load(plugins...); err != nil {
		return nil, err
	}
	for pattern, owner := range reg.Routes() {
		logger.Info("route", "pattern", pattern, "plugin", owner)
	}

	// Anything no route matched gets the same plain 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	return recoverPanics(logger, securityHeaders(limitBody(mux))), nil
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
				writeError(w, http.StatusInternalServerError, "internal error")
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

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
