package feature

import (
	"context"
	"net/http"
)

// VisitorHandler serves one request from a visitor: someone signed in at
// the identity provider who has no account here yet.
type VisitorHandler func(v Visitor, w http.ResponseWriter, r *http.Request)

// Visitor is what a module can do for a visitor. It learns nothing about
// them: it can only have the core set their account up, and the core
// answers them itself, with their new account or why not.
type Visitor interface {
	// Adopt sets the visitor's account up on an existing account of the
	// media backend, once its username and password check out, through the
	// account gates like any other creation. The account keeps its
	// history. With keepLogin, its old login keeps working, for a
	// transition; otherwise it's moved for good and its old login stops
	// working. The password is used once and never kept. Adopt reports
	// whether the account was set up.
	Adopt(w http.ResponseWriter, username, password string, keepLogin bool) bool
}

// SetupOffer is implemented by a module that offers visitors another way to
// set their account up than a new one: bringing an old one, say. While a
// module offers one, a first visit no longer creates an account by itself;
// it answers account-setup-required, which carries the offers, and the
// visitor chooses.
type SetupOffer interface {
	// Offer names the offer and its detail, as account-setup-required
	// carries them: "claim" and "move", say. ok is false when the module
	// offers nothing at the moment.
	Offer(h Host) (name, value string, ok bool)
}

// Adoption is the media backend's side of bringing existing accounts over,
// for control panel routes.
type Adoption interface {
	// Linked counts the adopted accounts whose old login still works.
	Linked(ctx context.Context) (int, error)
	// MoveLinked moves them for good, as adopting them without keepLogin
	// would have: their old logins stop working. It returns how many
	// moved; an error leaves the others for a retry.
	MoveLinked(ctx context.Context) (moved int, err error)
}
