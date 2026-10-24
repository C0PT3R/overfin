package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/creation"
	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/store"
)

// Invitations serves an invitation's link, https://site/invite/<code>,
// whatever the sign-in: it asks the invitations module whether the code
// works, and leaves it in this browser for the account's creation. With
// Overfin's own sign-in, the page is the registration form; with an
// identity provider, it says so and leads on, to the provider's sign-up
// link where it makes one.
type Invitations struct {
	Mounts []Mount
	// Managed is Overfin's own sign-in; nil with an identity provider.
	Managed *Managed
	Store   *store.Store
	Logger  *slog.Logger
}

func (Invitations) Name() string { return "invitations" }

func (v Invitations) Register(r *plugin.Registrar) error {
	return r.HandlePage("GET "+invitePage+"{code}", http.HandlerFunc(v.open))
}

func (v Invitations) open(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	valid, signUp := false, ""
	if inviter, h := inviterOf(v.Mounts); inviter != nil && len(code) <= 64 {
		var err error
		if valid, signUp, err = inviter.Invitation(r.Context(), h, code); err != nil {
			v.Logger.Error("reading an invitation failed", "err", err)
		}
	}
	if valid {
		c := &http.Cookie{Name: feature.InviteCookie, Value: code, MaxAge: 24 * 60 * 60}
		if v.Managed == nil {
			// Lax: it must survive the trip through an identity provider.
			c.SameSite = http.SameSiteLaxMode
		}
		cookie.Set(w, r, c)
	}
	if v.Managed != nil {
		v.Managed.InvitationPage(w, r, valid)
		return
	}
	if signUp == "" {
		signUp = "/"
	}
	renderPage(w, r, v.Store, v.Logger, page{Mode: "invited", Valid: valid, Continue: signUp})
}

// inviterOf is the module that keeps invitations, with its host; nil when
// it isn't switched on.
func inviterOf(mounts []Mount) (feature.Inviter, feature.Host) {
	for _, m := range mounts {
		if i, ok := m.Module.(feature.Inviter); ok {
			return i, &host{mount: m}
		}
	}
	return nil, nil
}

// markAdmitted marks a request as coming from someone Overfin's own
// sign-in let in: their account, when it's created, needs no invitation
// of its own.
func markAdmitted(ctx context.Context) context.Context { return creation.Admitted(ctx) }
