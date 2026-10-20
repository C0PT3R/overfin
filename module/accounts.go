package module

import (
	"context"
	"net/http"
	"time"

	"github.com/C0PT3R/overfin/internal/problem"
)

// AccountGate is implemented by a module with a say in creating accounts:
// whether one may be created for a visitor, and what it starts with. Both
// methods run under the core's account-creation lock, so two creations never
// race (two people can't both redeem a single-use invitation).
type AccountGate interface {
	// NewAccount runs before an account is created for the visitor making
	// r. Return a *Refusal to refuse it, a plan to shape it, or nil and nil
	// to have no opinion. Nothing is created yet.
	NewAccount(ctx context.Context, h Host, r *http.Request) (*AccountPlan, error)
	// AccountCreated runs once the account exists and the plan is applied,
	// with the plan this module gave (nil if none). An error undoes the
	// account.
	AccountCreated(ctx context.Context, h Host, r *http.Request, accountID string, plan *AccountPlan) error
}

// AccountPlan shapes a new account. Zero fields leave the defaults.
type AccountPlan struct {
	// Template is the account template's ID; empty for Default.
	Template string
	// Expires is when the account's access ends; zero for never.
	Expires time.Time
	// Label is the operator's label for it, shown in the control panel.
	Label string
	// Profiles are created with the account, besides the owner's.
	Profiles []StarterProfile
	// Ref is the module's own reference (an invitation's ID, say), given
	// back to AccountCreated.
	Ref string
}

// StarterProfile is a profile created with an account.
type StarterProfile struct {
	Name string
	// Kind is "standard" or "kids"; a kids profile needs RatingCeiling, one
	// of the server's rating names.
	Kind          string
	RatingCeiling string
}

// Refusal refuses an account; the visitor is answered with Type.
type Refusal struct {
	Type   ProblemType
	Detail string
}

func (r *Refusal) Error() string { return "module: account refused: " + string(r.Type) }

// InvitationRequired refuses an account for want of a valid invitation.
const InvitationRequired ProblemType = problem.InvitationRequired
