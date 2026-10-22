package overfin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/store"
)

// importPeople runs -import-people: before moving from an identity provider
// to managed sign-in, everyone the provider has, with an email, gets to
// sign in with it, under the user ID they already have, so their account
// stays theirs. Those whose last login put them in the control panel's
// group are admins. Run it with the provider's configuration still in
// place ([connector]); people already there are left as they are.
func importPeople(ctx context.Context, cfg *config.Config, db *store.Store, w io.Writer) error {
	cn := cfg.Connector
	if !cn.Enabled() {
		return errors.New("-import-people reads people from the identity provider: it needs [connector] in the configuration")
	}
	dir := connector.New(cn.Kind, cn.URL, cfg.Secrets.ConnectorToken, oidc.NewClient(cn.Dial))
	return importFrom(ctx, dir, cfg.Admin.Group, db, w)
}

// importFrom imports the people of dir; adminGroup names the admins' group.
func importFrom(ctx context.Context, dir connector.Directory, adminGroup string, db *store.Store, w io.Writer) error {
	people, err := dir.People(ctx)
	if err != nil {
		return fmt.Errorf("reading people from the identity provider: %w", err)
	}
	admins := map[string]bool{}
	if g := adminGroup; g != "" {
		if admins, err = db.SubjectsInGroup(ctx, g); err != nil {
			return err
		}
	}
	var added, adminCount, skipped int
	for subject, p := range people {
		a, err := mail.ParseAddress(p.Email)
		if !p.Active || err != nil || a.Address != p.Email {
			skipped++
			continue
		}
		if _, err := db.UserBySubject(ctx, subject); err == nil {
			skipped++ // already there
			continue
		}
		switch err := db.ImportUser(ctx, subject, p.Email, admins[subject]); {
		case errors.Is(err, store.ErrEmailTaken):
			skipped++
			continue
		case err != nil:
			return err
		}
		added++
		if admins[subject] {
			adminCount++
		}
	}
	fmt.Fprintf(w, "%d people imported (%d admins); %d left out (no usable email, disabled, or already there).\n",
		added, adminCount, skipped)
	if adminCount == 0 {
		fmt.Fprintln(w, "No admin was found among them: with managed sign-in, the setup page will ask for one.")
	}
	return nil
}

// signinLink runs -signin-link: a one-time sign-in link, valid a day, for
// someone who can't sign in otherwise (no email server, a lost password).
// Whoever runs it has the server in hand already.
func signinLink(ctx context.Context, cfg *config.Config, db *store.Store, k *keys.Keys, email string, w io.Writer) error {
	if cfg.Identity.Mode != "managed" {
		return errors.New("-signin-link is for managed sign-in (identity.mode = \"managed\")")
	}
	u, err := db.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("nobody signs in with %s here", email)
	}
	if err != nil {
		return err
	}
	if u.Disabled {
		return fmt.Errorf("%s is disabled", email)
	}
	m := &api.Managed{Store: db, CodeKey: k.SigninCode, Logger: slog.New(slog.DiscardHandler)}
	path, err := m.SigninLink(ctx, u.Email, 24*time.Hour)
	if err != nil {
		return err
	}
	if cfg.Site.URL == "" {
		fmt.Fprintf(w, "Open this on the site's address, once, within a day: %s\n", path)
		return nil
	}
	fmt.Fprintf(w, "Open this once, within a day: %s%s\n", cfg.Site.URL, path)
	return nil
}
