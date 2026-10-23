package feature

import (
	"net/http"
)

// VisitorHandler serves one request from a visitor: someone signed in at
// the identity provider who has no account here yet.
type VisitorHandler func(v Visitor, w http.ResponseWriter, r *http.Request)

// Visitor is what a module can do for a visitor. It learns nothing about
// them: it can only have the core set their account up, and the core
// answers them itself, with their new account or why not.
type Visitor interface {
	// Copy sets the visitor's account up as a new one, with the history
	// their account had on another Jellyfin server, at from, once its
	// username and password check out there, through the account gates
	// like any other creation. Nothing changes on that server; the
	// password is used for that one sign-in and never kept. Copy reports
	// whether the account was set up.
	Copy(w http.ResponseWriter, from, username, password string) bool
}

// SetupOffer is implemented by a module that offers visitors another way to
// set their account up than a new one: bringing an old one, say. While a
// module offers one, a first visit no longer creates an account by itself;
// it answers account-setup-required, which carries the offers, and the
// visitor chooses.
type SetupOffer interface {
	// Offer names the offer and its detail, as account-setup-required
	// carries them: "claim" and "copy", say. ok is false when the module
	// offers nothing at the moment.
	Offer(h Host) (name, value string, ok bool)
}
