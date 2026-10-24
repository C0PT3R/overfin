package overfin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/password"
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

// setPassword runs -set-password: a new password for someone who can't
// sign in otherwise (an admin locked out, no email server), read from the
// terminal, following the password rules. Their other logins end. Whoever
// runs it has the server in hand already.
func setPassword(ctx context.Context, cfg *config.Config, db *store.Store, email string, in io.Reader, w io.Writer) error {
	if cfg.Identity.Mode != "managed" {
		return errors.New("-set-password is for managed sign-in (identity.mode = \"managed\")")
	}
	u, err := db.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("nobody signs in with %s here", email)
	}
	if err != nil {
		return err
	}
	rules, err := db.PasswordRules(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "New password for %s (%d characters at least): ", u.Email, rules.MinLength)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no password given")
	}
	pw := strings.TrimRight(line, "\r\n")
	if lacks := rules.Lacks(pw); lacks != "" {
		return fmt.Errorf("that password lacks: %s", lacks)
	}
	hash, err := password.Hash(pw)
	if err != nil {
		return err
	}
	if err := db.SetPassword(ctx, u.Subject, hash); err != nil {
		return err
	}
	if _, err := db.ConfirmUser(ctx, u.Subject); err != nil {
		return err
	}
	if err := db.SetMustChange(ctx, u.Subject, false); err != nil {
		return err
	}
	if err := db.DeleteLogins(ctx, u.Subject); err != nil {
		return err
	}
	fmt.Fprintln(w, "\nDone: sign in with it.")
	return nil
}
