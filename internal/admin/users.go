package admin

import (
	"context"

	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/store"
)

// Users is who the people behind accounts are with managed sign-in: the
// people Overfin signs in itself, by email.
type Users struct{ Store *store.Store }

// People returns everyone, by subject.
func (u Users) People(ctx context.Context) (map[string]connector.Person, error) {
	all, err := u.Store.Users(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]connector.Person, len(all))
	for _, p := range all {
		out[p.Subject] = connector.Person{Username: p.Email, Email: p.Email, Active: !p.Disabled}
	}
	return out, nil
}
