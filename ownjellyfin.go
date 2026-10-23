package overfin

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/overfin/server/internal/admin"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/gpu"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/jfsettings"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/takeover"
)

// jellyfinStartup is the language and country a fresh Jellyfin gets.
var jellyfinStartup = jellyfin.Startup{UICulture: "en-US", Country: "US", Language: "en"}

// jellyfinWait is how long Overfin waits for a Jellyfin it has no key for
// yet to answer, at start: one started with it (same compose) takes a
// moment.
const jellyfinWait = 2 * time.Minute

// ownJellyfin finds Overfin's own Jellyfin at start (SECURITY.md rule 67):
// at the configuration file's address, or the one saved, with the key
// Overfin made on it, as saved. A fresh server is set up at once. ok is
// false when there's none yet, or when the operator has to hand it over on
// the setup page.
func ownJellyfin(ctx context.Context, cfg *config.Config, db *store.Store, logger *slog.Logger) (url, key string, ok bool, err error) {
	savedURL, savedKey, _, err := db.Jellyfin(ctx)
	if err != nil {
		// Sealed with a lost secret key: like no key at all.
		logger.Warn("Overfin's key for Jellyfin can't be read: hand Jellyfin over again on the setup page", "err", err)
		savedURL, savedKey = "", ""
	}
	serverID, err := db.JellyfinServer(ctx)
	if err != nil {
		return "", "", false, err
	}
	url = cmp.Or(cfg.Jellyfin.URL, savedURL)
	if url == "" {
		return "", "", false, nil
	}
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		return "", "", false, err
	}
	found, err := takeover.Look(ctx, jf, savedKey, serverID)
	// Without a key yet, Jellyfin may just be starting too.
	for deadline := time.Now().Add(jellyfinWait); err != nil && savedKey == "" && time.Now().Before(deadline); {
		logger.Info("waiting for Jellyfin to answer", "jellyfin", url)
		select {
		case <-ctx.Done():
			return "", "", false, ctx.Err()
		case <-time.After(5 * time.Second):
		}
		found, err = takeover.Look(ctx, jf, savedKey, serverID)
	}
	if err != nil {
		if savedKey != "" {
			logger.Warn("Jellyfin doesn't answer: Overfin starts with what it saved, and tries again as it goes", "err", err)
			return url, savedKey, true, nil
		}
		logger.Warn("Jellyfin doesn't answer: the setup page asks again", "err", err)
		return "", "", false, nil
	}
	switch found.State {
	case takeover.Fresh:
		key, err := takeover.SetUp(ctx, jf, jellyfinStartup)
		if err != nil {
			return "", "", false, err
		}
		if err := db.SetJellyfin(ctx, url, key, found.Server.ID); err != nil {
			return "", "", false, err
		}
		logger.Info("Overfin set up its Jellyfin, which was fresh", "jellyfin", url, "version", found.Server.Version)
		return url, key, true, nil
	case takeover.Ours:
		if url != savedURL || serverID == "" {
			if err := db.SetJellyfin(ctx, url, savedKey, found.Server.ID); err != nil {
				return "", "", false, err
			}
		}
		if n, err := takeover.Tidy(ctx, jf, jellyfin.Auth{Token: savedKey, DeviceID: "overfin-admin"}, savedKey); err != nil {
			logger.Warn("Overfin's older keys on Jellyfin couldn't be revoked", "err", err)
		} else if n > 0 {
			logger.Info("Overfin's older keys on Jellyfin were revoked", "keys", n)
		}
		return url, savedKey, true, nil
	default:
		logger.Warn("this Jellyfin was set up by someone else, or Overfin's key for it is gone: "+
			"Overfin takes it only if it's handed over, on the setup page", "jellyfin", url)
		return "", "", false, nil
	}
}

// takeOver is the setup page's Jellyfin step: the server at fixed (the
// configuration file's address), else at the address asked; set up if
// fresh, taken back if it answers to Overfin's key, and otherwise first
// shown, then handed over by its administrator. When ans.Done, url and key
// are saved.
func takeOver(ctx context.Context, db *store.Store, fixed string, req admin.JellyfinRequest, logger *slog.Logger) (url, key string, ans admin.JellyfinAnswer, err error) {
	url = fixed
	if url == "" {
		url = strings.TrimRight(strings.TrimSpace(req.URL), "/")
		if err := config.CheckJellyfinURL(url); err != nil {
			return "", "", ans, problemf("Jellyfin's address %s", err)
		}
	}
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		return "", "", ans, problemf("Jellyfin's address: %v", err)
	}
	savedKey := ""
	if _, k, ok, err := db.Jellyfin(ctx); err == nil && ok {
		savedKey = k
	}
	serverID, err := db.JellyfinServer(ctx)
	if err != nil {
		return "", "", ans, err
	}
	found, err := takeover.Look(ctx, jf, savedKey, serverID)
	if err != nil {
		return "", "", ans, err
	}
	ans.Server = admin.JellyfinServer{ID: found.Server.ID, Name: found.Server.Name, Version: found.Server.Version}
	switch found.State {
	case takeover.Fresh:
		key, err = takeover.SetUp(ctx, jf, jellyfinStartup)
	case takeover.Ours:
		key = savedKey
	default:
		switch {
		case req.Username == "":
			ans.NeedsLogin = true
			return "", "", ans, nil
		case req.Confirm != found.Server.ID:
			// Shown first; handed over only once the operator confirmed
			// this very server.
			s, err := takeover.Inspect(ctx, jf, req.Username, req.Password)
			if err != nil {
				return "", "", ans, err
			}
			ans.Survey = &admin.JellyfinSurvey{Users: s.Users, Libraries: s.Libraries}
			return "", "", ans, nil
		}
		key, err = takeover.HandOver(ctx, jf, req.Username, req.Password)
	}
	if err != nil {
		return "", "", ans, err
	}
	// Saved before access is closed: Overfin never loses its way in.
	if err := db.SetJellyfin(ctx, url, key, found.Server.ID); err != nil {
		return "", "", ans, err
	}
	if found.State == takeover.Other {
		if err := closeAfterHandOver(ctx, db, jf, key, logger); err != nil {
			return "", "", ans, err
		}
	}
	how := map[takeover.State]string{takeover.Fresh: "set up", takeover.Ours: "found again", takeover.Other: "handed over"}[found.State]
	logger.Info("setup page: Overfin's Jellyfin is "+how, "jellyfin", url)
	ans.Done = true
	ans.Summary = fmt.Sprintf("Jellyfin %s, at %s: %s.", found.Server.Version, url, how)
	return url, key, ans, nil
}

// transcodingSettings are the Jellyfin settings of the GPU family chosen;
// none until one is.
func transcodingSettings(family, device string) []jfsettings.Setting {
	f, ok := gpu.Find(family)
	if !ok {
		return nil
	}
	return f.Settings(device)
}

// forPage makes errors written for people admin.Problems, which the setup
// page shows as they are; it only says other errors failed, and logs them.
func forPage(err error) error {
	var p problem
	var tp takeover.Problem
	switch {
	case errors.As(err, &p):
		return admin.Problem(p)
	case errors.As(err, &tp):
		return admin.Problem(tp)
	}
	return err
}

// handOverInTerminal hands Overfin's Jellyfin over again, asking the
// operator in the terminal: its address (unless fixed), an administrator's
// login, and a confirmation once they see what it holds.
func handOverInTerminal(ctx context.Context, db *store.Store, fixed string, in io.Reader, out io.Writer) (url, key string, err error) {
	lines := bufio.NewScanner(in)
	ask := func(question string) string {
		fmt.Fprint(out, question)
		lines.Scan()
		return strings.TrimSpace(lines.Text())
	}
	fmt.Fprintln(out, "Overfin's key for its Jellyfin was sealed with the lost secret key: hand that Jellyfin over again.")
	url = fixed
	if url == "" {
		url = strings.TrimRight(ask("Jellyfin's address, as Overfin reaches it: "), "/")
	}
	if err := config.CheckJellyfinURL(url); err != nil {
		return "", "", problemf("Jellyfin's address %s", err)
	}
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		return "", "", err
	}
	found, err := takeover.Look(ctx, jf, "", "")
	if err != nil {
		return "", "", err
	}
	if found.State == takeover.Fresh {
		return "", "", errors.New("that Jellyfin is fresh: it holds nothing of Overfin's; start Overfin, which sets it up")
	}
	username := ask("Its administrator's username: ")
	password := ask("Password (it shows as you type): ")
	survey, err := takeover.Inspect(ctx, jf, username, password)
	if err != nil {
		return "", "", err
	}
	fmt.Fprintf(out, "Jellyfin %s (%s), at %s: %d users, %d libraries.\n",
		found.Server.Version, found.Server.Name, url, len(survey.Users), survey.Libraries)
	if a := ask("Is it dedicated to Overfin, for Overfin to take over? Type yes: "); a != "yes" {
		return "", "", errors.New("not handed over")
	}
	if key, err = takeover.HandOver(ctx, jf, username, password); err != nil {
		return "", "", err
	}
	// Saved before access is closed: Overfin never loses its way in.
	if err := db.SetJellyfin(ctx, url, key, found.Server.ID); err != nil {
		return "", "", err
	}
	if err := closeAfterHandOver(ctx, db, jf, key, slog.New(slog.NewTextHandler(out, nil))); err != nil {
		return "", "", err
	}
	return url, key, nil
}

// ownSettings sets back the Jellyfin settings Overfin owns, those of the
// GPU chosen included, and logs each it changed. A failure doesn't stop
// Overfin: it tries again at its next start.
func ownSettings(ctx context.Context, db *store.Store, url, key string, logger *slog.Logger) {
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		return
	}
	family, device, err := db.Transcoding(ctx)
	if err != nil {
		logger.Warn("the GPU chosen can't be read", "err", err)
	}
	changes, err := jfsettings.Apply(ctx, jf, jellyfin.Auth{Token: key, DeviceID: "overfin-admin"}, transcodingSettings(family, device)...)
	for _, c := range changes {
		logger.Info("a Jellyfin setting was set back to Overfin's", "change", c.String())
	}
	if err != nil {
		logger.Warn("setting Jellyfin's settings back failed; Overfin tries again at its next start", "err", err)
	}
}

// closeAfterHandOver closes access to a Jellyfin just handed over, as to
// every Jellyfin of Overfin's (SECURITY.md rule 67), and revokes the keys
// earlier takeovers made there.
func closeAfterHandOver(ctx context.Context, db *store.Store, jf *jellyfin.Client, key string, logger *slog.Logger) error {
	a := jellyfin.Auth{Token: key, DeviceID: "overfin-admin"}
	if err := takeover.CloseAccess(ctx, jf, a); err != nil {
		return err
	}
	if err := db.SetJellyfinAccess(ctx, false); err != nil {
		return err
	}
	if n, err := takeover.Tidy(ctx, jf, a, key); err != nil {
		logger.Warn("Overfin's older keys on Jellyfin couldn't be revoked", "err", err)
	} else if n > 0 {
		logger.Info("Overfin's older keys on Jellyfin were revoked", "keys", n)
	}
	return nil
}
