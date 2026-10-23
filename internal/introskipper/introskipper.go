// Package introskipper installs Intro Skipper on Overfin's Jellyfin, when
// the operator asks: a Jellyfin plugin that finds each episode's intro and
// credits, which Overfin's player then offers to skip. Its version is the
// one Overfin was tested with, and checked against the checksum Overfin
// knows before Jellyfin installs it.
package introskipper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// The version Overfin installs, and where it comes from.
const (
	Name       = "Intro Skipper"
	GUID       = "c83d86bb-a1e0-4c35-a113-e2101cf4ee6b"
	Version    = "12.0.5.0"
	Checksum   = "d3fc1c17efa61ed85e86228f3c2c940f"
	Repository = "https://intro-skipper.org/manifest.json"
)

// Problem is what's wrong, in plain words for the operator.
type Problem string

func (p Problem) Error() string { return string(p) }

// Status is where Intro Skipper stands on a server.
type Status struct {
	// Installed is true once it's there, whatever its version.
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// State is Jellyfin's: Active, Restart, Disabled, Malfunctioned…
	State string `json:"state,omitempty"`
}

// Look says whether Intro Skipper is on the server.
func Look(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth) (Status, error) {
	plugins, err := jf.Plugins(ctx, admin)
	if err != nil {
		return Status{}, err
	}
	// Jellyfin lists every version it has, the ones another replaced
	// included: the active one says most.
	want, _ := jellyfin.NormalizeID(GUID)
	var st Status
	for _, p := range plugins {
		if id, err := jellyfin.NormalizeID(p.ID); err != nil || id != want {
			continue
		}
		if !st.Installed || p.Status == "Active" || st.State == "Superseded" {
			st = Status{Installed: true, Version: p.Version, State: p.Status}
		}
	}
	return st, nil
}

// poll is how often the install is checked on; wait, how long Jellyfin
// gets to install it, then to restart. Tests shorten them.
var (
	poll = 2 * time.Second
	wait = 3 * time.Minute
)

// Install installs Intro Skipper and restarts Jellyfin, telling each step
// to progress; it returns once the plugin is active. One already there,
// whatever its version, is left as it is.
func Install(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth, progress func(string)) (Status, error) {
	st, err := Look(ctx, jf, admin)
	if err != nil || (st.Installed && st.State != "Restart") {
		return st, err
	}
	if !st.Installed {
		progress("Adding Intro Skipper's repository to Jellyfin…")
		repos, err := jf.Repositories(ctx, admin)
		if err != nil {
			return st, err
		}
		if !hasRepository(repos) {
			repos = append(repos, jellyfin.Repository{Name: Name, URL: Repository, Enabled: true})
			if err := jf.SetRepositories(ctx, admin, repos); err != nil {
				return st, err
			}
		}
		progress("Checking version " + Version + "…")
		versions, err := jf.PackageVersions(ctx, admin, Name, GUID)
		if err != nil {
			return st, fmt.Errorf("listing Intro Skipper's versions: %w", err)
		}
		if err := checkVersion(versions); err != nil {
			return st, err
		}
		progress("Installing version " + Version + "…")
		if err := jf.InstallPackage(ctx, admin, Name, GUID, Version, Repository); err != nil {
			return st, err
		}
		if st, err = until(ctx, func() (Status, bool, error) {
			st, err := Look(ctx, jf, admin)
			return st, st.Installed, err
		}); err != nil {
			return st, Problem("Jellyfin didn't install Intro Skipper in time: its log may say why.")
		}
	}
	progress("Restarting Jellyfin…")
	if err := jf.Restart(ctx, admin); err != nil {
		return st, err
	}
	// While it restarts, Jellyfin doesn't answer: errors mean "not yet".
	st, err = until(ctx, func() (Status, bool, error) {
		st, err := Look(ctx, jf, admin)
		return st, err == nil && st.State != "Restart", nil
	})
	if err != nil {
		return st, Problem("Jellyfin didn't come back in time after its restart.")
	}
	if st.State != "Active" {
		return st, Problem("Jellyfin restarted, but Intro Skipper's state is " + st.State + ": Jellyfin's log may say why.")
	}
	return st, nil
}

// Remove uninstalls Intro Skipper, takes its repository off Jellyfin's
// list and restarts Jellyfin, telling each step to progress.
func Remove(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth, progress func(string)) (Status, error) {
	plugins, err := jf.Plugins(ctx, admin)
	if err != nil {
		return Status{}, err
	}
	want, _ := jellyfin.NormalizeID(GUID)
	for _, p := range plugins {
		if id, err := jellyfin.NormalizeID(p.ID); err == nil && id == want {
			progress("Uninstalling Intro Skipper…")
			if err := jf.UninstallPlugin(ctx, admin, id, p.Version); err != nil {
				return Status{}, err
			}
		}
	}
	repos, err := jf.Repositories(ctx, admin)
	if err != nil {
		return Status{}, err
	}
	if hasRepository(repos) {
		progress("Removing its repository…")
		kept := repos[:0]
		for _, r := range repos {
			if !strings.EqualFold(strings.TrimSpace(r.URL), Repository) {
				kept = append(kept, r)
			}
		}
		if err := jf.SetRepositories(ctx, admin, kept); err != nil {
			return Status{}, err
		}
	}
	progress("Restarting Jellyfin…")
	if err := jf.Restart(ctx, admin); err != nil {
		return Status{}, err
	}
	st, err := until(ctx, func() (Status, bool, error) {
		pending, err := jf.PendingRestart(ctx, admin)
		if err != nil || pending {
			return Status{}, false, nil
		}
		st, err := Look(ctx, jf, admin)
		return st, err == nil, nil
	})
	if err != nil {
		return st, Problem("Jellyfin didn't come back in time after its restart.")
	}
	if st.Installed {
		return st, Problem("Jellyfin restarted, but Intro Skipper is still there: Jellyfin's log may say why.")
	}
	return st, nil
}

func hasRepository(repos []jellyfin.Repository) bool {
	for _, r := range repos {
		if strings.EqualFold(strings.TrimSpace(r.URL), Repository) && r.Enabled {
			return true
		}
	}
	return false
}

// checkVersion finds the version Overfin installs, with the checksum it
// knows.
func checkVersion(versions []jellyfin.PackageVersion) error {
	for _, v := range versions {
		if v.Version != Version {
			continue
		}
		if !strings.EqualFold(v.Checksum, Checksum) {
			return Problem("Intro Skipper's version " + Version + " isn't the one Overfin knows (its checksum differs): nothing was installed.")
		}
		return nil
	}
	return Problem("Intro Skipper's repository doesn't offer version " + Version + " any more: nothing was installed.")
}

// until checks every poll until done says so, for at most wait.
func until(ctx context.Context, check func() (Status, bool, error)) (Status, error) {
	deadline := time.Now().Add(wait)
	for {
		st, ok, err := check()
		if err != nil || ok {
			return st, err
		}
		if time.Now().After(deadline) {
			return st, errors.New("introskipper: timed out")
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Installer runs one install at a time in the background, for the setup
// wizard and the control panel, and says where it stands.
type Installer struct {
	// Jellyfin is Overfin's Jellyfin and its key, when it has one.
	Jellyfin func() (*jellyfin.Client, jellyfin.Auth, bool)
	Logger   *slog.Logger

	mu       sync.Mutex
	running  bool
	step     string
	err      error
	finished bool
	status   Status
}

// Progress is where an install or a removal stands.
type Progress struct {
	Running bool   `json:"running"`
	Step    string `json:"step,omitempty"`
	Done    bool   `json:"done"`
	Status  Status `json:"status"`
	// Error is what went wrong the last time, in plain words.
	Error string `json:"error,omitempty"`
}

// Start begins an install, unless one is running.
func (in *Installer) Start() error { return in.start(false) }

// StartRemoving begins removing Intro Skipper, unless an install or a
// removal is running.
func (in *Installer) StartRemoving() error { return in.start(true) }

func (in *Installer) start(remove bool) error {
	jf, admin, ok := in.Jellyfin()
	if !ok {
		return Problem("Overfin has no Jellyfin of its own yet.")
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.running {
		return nil
	}
	in.running, in.finished, in.err, in.step = true, false, nil, "Starting…"
	go func() {
		do, done, what := Install, "Intro Skipper is installed on Overfin's Jellyfin", "installing"
		if remove {
			do, done, what = Remove, "Intro Skipper is removed from Overfin's Jellyfin", "removing"
		}
		st, err := do(context.Background(), jf, admin, func(step string) {
			in.mu.Lock()
			in.step = step
			in.mu.Unlock()
		})
		var p Problem
		switch {
		case err == nil:
			in.Logger.Info(done, "version", st.Version)
		case errors.As(err, &p):
			in.Logger.Warn(what+" Intro Skipper didn't happen", "why", err)
		default:
			in.Logger.Error(what+" Intro Skipper failed", "err", err)
			err = Problem(strings.ToUpper(what[:1]) + what[1:] + " Intro Skipper failed; Overfin's log says why.")
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		in.running, in.finished, in.status, in.err, in.step = false, true, st, err, ""
	}()
	return nil
}

// Look says whether Intro Skipper is on Overfin's Jellyfin.
func (in *Installer) Look(ctx context.Context) (Status, error) {
	jf, admin, ok := in.Jellyfin()
	if !ok {
		return Status{}, nil
	}
	return Look(ctx, jf, admin)
}

// Progress says where the install stands.
func (in *Installer) Progress() Progress {
	in.mu.Lock()
	defer in.mu.Unlock()
	p := Progress{Running: in.running, Step: in.step, Done: in.finished && in.err == nil, Status: in.status}
	if in.err != nil {
		p.Error = in.err.Error()
	}
	return p
}
