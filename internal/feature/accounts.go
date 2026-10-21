package feature

import (
	"context"
	"net/http"
	"time"

	"github.com/C0PT3R/overfin-server/internal/creation"
	"github.com/C0PT3R/overfin-server/internal/problem"
)

// AccountGate is implemented by a module with a say in creating accounts:
// whether one may be created for a visitor, and what it starts with. Both
// methods run under the core's account-creation lock, so two creations never
// race (two people can't both redeem a single-use invitation).
type AccountGate interface {
	// NewAccount runs before an account is created for the visitor making
	// r. Return a *Refusal to refuse it, a plan to shape it, or nil and nil
	// to have no opinion. Nothing is created yet. Adopting(ctx) tells
	// whether the visitor is bringing an existing account over.
	NewAccount(ctx context.Context, h Host, r *http.Request) (*AccountPlan, error)
	// AccountCreated runs once the account exists and the plan is applied,
	// with the plan this module gave (nil if none). An error undoes the
	// account.
	AccountCreated(ctx context.Context, h Host, r *http.Request, accountID string, plan *AccountPlan) error
}

// AccountPlan shapes a new account. Zero fields leave the defaults. An
// account always starts with its owner profile alone: the template sets how
// many more its owner may create, and the owner creates them.
type AccountPlan struct {
	// Template is the account template's ID; empty for Default.
	Template string
	// Expires is when the account's access ends; zero for never.
	Expires time.Time
	// Label is the operator's label for it, shown in the control panel.
	Label string
	// Ref is the module's own reference (an invitation's ID, say), given
	// back to AccountCreated.
	Ref string
}

// Refusal refuses an account; the visitor is answered with Type.
type Refusal struct {
	Type   ProblemType
	Detail string
}

func (r *Refusal) Error() string { return "module: account refused: " + string(r.Type) }

// InvitationRequired refuses an account for want of a valid invitation.
const InvitationRequired ProblemType = problem.InvitationRequired

// Adopting reports, in an AccountGate's methods, whether the account is
// being created by bringing an existing one over (Visitor.Adopt) rather
// than as a new one.
func Adopting(ctx context.Context) bool { return creation.IsAdopting(ctx) }

// AccountTemplate is an account template: what an account on it may do.
type AccountTemplate struct {
	ID          string
	Name        string
	MaxStreams  int
	MaxProfiles int
}
