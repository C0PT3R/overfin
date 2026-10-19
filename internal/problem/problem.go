// Package problem writes error responses as RFC 9457 problem details.
//
// Every error type Overfin can return is listed here, with its HTTP status
// and title, so the API's errors stay consistent and documented in one place.
// Clients branch on Type; Detail is for humans only.
package problem

import (
	"encoding/json"
	"net/http"
)

// Type is a stable, machine-readable error name.
type Type string

const (
	NotFound         Type = "not-found"
	NotAuthenticated Type = "not-authenticated"
	OwnerOnly        Type = "owner-only"
	InvalidRequest   Type = "invalid-request"
	InvalidCursor    Type = "invalid-cursor"
	RateLimited      Type = "rate-limited"
	Internal         Type = "internal-error"
)

type info struct {
	status int
	title  string
}

// registry holds the status and title of every type. The title is the same
// for every occurrence of a type.
var registry = map[Type]info{
	NotFound:         {http.StatusNotFound, "Not found"},
	NotAuthenticated: {http.StatusUnauthorized, "Not logged in"},
	OwnerOnly:        {http.StatusForbidden, "Only the account owner can do this"},
	InvalidRequest:   {http.StatusBadRequest, "Invalid request"},
	InvalidCursor:    {http.StatusBadRequest, "Invalid cursor"},
	RateLimited:      {http.StatusTooManyRequests, "Too many requests"},
	Internal:         {http.StatusInternalServerError, "Internal error"},
}

// Problem is one error response. Extra fields specific to a type (such as
// stepUpUrl) are added later, as their features arrive.
type Problem struct {
	Type   Type   `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// New returns the problem for t, with an optional human-readable detail.
// The detail must never contain upstream error text or internal details
// (SECURITY.md rule 31).
func New(t Type, detail string) Problem {
	i, ok := registry[t]
	if !ok {
		// An unregistered type is a programming error; never leak it.
		t, detail = Internal, ""
		i = registry[Internal]
	}
	return Problem{Type: t, Title: i.title, Status: i.status, Detail: detail}
}

// Write sends p as the response.
func Write(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// Send is New followed by Write.
func Send(w http.ResponseWriter, t Type, detail string) {
	Write(w, New(t, detail))
}
