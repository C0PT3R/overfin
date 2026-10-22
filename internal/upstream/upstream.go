// Package upstream holds what every client of an upstream service shares.
package upstream

import (
	"errors"
	"net/url"
)

// Plain is a failed request's error without its URL. Go's HTTP client
// writes the whole address into its errors, query included, which can
// carry a token (Jellyfin's stream URLs) or what someone searched for;
// logs never get either (SECURITY.md rules 48 and 54). What went wrong
// stays, and errors.Is still sees through it (a cancelled request, a
// deadline).
func Plain(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return &plainError{op: ue.Op, err: ue.Err}
	}
	return err
}

type plainError struct {
	op  string
	err error
}

func (e *plainError) Error() string { return e.op + ": " + e.err.Error() }
func (e *plainError) Unwrap() error { return e.err }
