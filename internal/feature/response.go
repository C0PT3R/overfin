package feature

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
)

// Title is a title in the API's own format (see api/openapi.yaml,
// TitleSummary): what clients already understand.
type Title = library.TitleSummary

// ErrNotFound: the title doesn't exist, or this profile may not see it. The
// two are never told apart.
var ErrNotFound = errors.New("module: not found")

// ProblemType is a stable error name, such as "not-found".
type ProblemType = problem.Type

// The problem types modules answer with most. Clients branch on them.
const (
	NotFound       ProblemType = problem.NotFound
	InvalidRequest ProblemType = problem.InvalidRequest
	OwnerOnly      ProblemType = problem.OwnerOnly
	RateLimited    ProblemType = problem.RateLimited
	Internal       ProblemType = problem.Internal
)

// Problem answers with an error in the API's standard form (RFC 9457). The
// detail is for humans: never upstream error text or internal details.
func Problem(w http.ResponseWriter, t ProblemType, detail string) {
	problem.Send(w, t, detail)
}

// JSON answers 200 with v encoded as JSON.
func JSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// NoContent answers 204.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// TitleID reads a title ID from a request path ("title_…"), as the backend
// takes it: only its shape is checked, the backend checks the rest. ok is
// false for anything else, which should be answered with NotFound.
func TitleID(s string) (id string, ok bool) {
	if !strings.HasPrefix(s, library.TitlePrefix) || len(s) > 128 {
		return "", false
	}
	return s, true
}
