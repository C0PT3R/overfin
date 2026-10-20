// Package creation marks how an account is being created, for the module
// contract's account gates. Only the core sets the mark.
package creation

import "context"

type adoptingKey struct{}

// Adopting marks ctx as creating an account by bringing an existing one
// over.
func Adopting(ctx context.Context) context.Context {
	return context.WithValue(ctx, adoptingKey{}, true)
}

// IsAdopting reports whether ctx was marked by Adopting.
func IsAdopting(ctx context.Context) bool {
	v, _ := ctx.Value(adoptingKey{}).(bool)
	return v
}
