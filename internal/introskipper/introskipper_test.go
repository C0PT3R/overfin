package introskipper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// fakeJellyfin installs plugins the way Jellyfin does: from a repository it
// lists, waiting for a restart, during which it doesn't answer.
type fakeJellyfin struct {
	mu        sync.Mutex
	repos     []jellyfin.Repository
	checksum  string
	plugin    *jellyfin.Plugin
	down      int // requests still refused, restarting
	installed []string
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down > 0 {
		f.down--
		if f.down == 0 && f.plugin != nil {
			f.plugin.Status = "Active"
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /Plugins":
		out := []jellyfin.Plugin{}
		if f.plugin != nil {
			out = append(out, *f.plugin)
		}
		json.NewEncoder(w).Encode(out)
	case "GET /Repositories":
		json.NewEncoder(w).Encode(f.repos)
	case "POST /Repositories":
		json.NewDecoder(r.Body).Decode(&f.repos)
		w.WriteHeader(http.StatusNoContent)
	case "GET /Packages/Intro Skipper":
		if !hasRepository(f.repos) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"versions":[{"version":"12.0.5.0","checksum":"`+f.checksum+`","sourceUrl":"https://example.org/x.zip"}]}`)
	case "POST /Packages/Installed/Intro Skipper":
		q := r.URL.Query()
		f.installed = append(f.installed, q.Get("version")+" "+q.Get("assemblyGuid")+" "+q.Get("repositoryUrl"))
		f.plugin = &jellyfin.Plugin{ID: "c83d86bba1e04c35a113e2101cf4ee6b", Name: Name, Version: q.Get("version"), Status: "Restart"}
		w.WriteHeader(http.StatusNoContent)
	case "POST /System/Restart":
		f.down = 3
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T, f *fakeJellyfin) (*jellyfin.Client, jellyfin.Auth) {
	t.Helper()
	poll, wait = time.Millisecond, time.Second
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	jf, err := jellyfin.New(srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	return jf, jellyfin.Auth{Token: "admin", DeviceID: "overfin-admin"}
}

func TestInstall(t *testing.T) {
	stable := jellyfin.Repository{Name: "Jellyfin Stable", URL: "https://repo.jellyfin.org/files/plugin/manifest.json", Enabled: true}
	f := &fakeJellyfin{repos: []jellyfin.Repository{stable}, checksum: Checksum}
	jf, admin := setup(t, f)
	var steps []string
	st, err := Install(context.Background(), jf, admin, func(s string) { steps = append(steps, s) })
	if err != nil || !st.Installed || st.State != "Active" || st.Version != Version {
		t.Fatalf("install: %+v %v", st, err)
	}
	if len(f.repos) != 2 || f.repos[0] != stable || f.repos[1].URL != Repository {
		t.Errorf("repositories: %+v", f.repos)
	}
	if len(f.installed) != 1 || f.installed[0] != Version+" "+GUID+" "+Repository {
		t.Errorf("installed: %v", f.installed)
	}
	if len(steps) != 4 || !strings.Contains(steps[3], "Restarting") {
		t.Errorf("steps: %q", steps)
	}
	// There already: nothing more.
	if st, err := Install(context.Background(), jf, admin, func(string) {}); err != nil || len(f.installed) != 1 || st.State != "Active" {
		t.Errorf("again: %+v %v, installed %v", st, err, f.installed)
	}
}

// A version that isn't the one Overfin knows isn't installed.
func TestInstallChecksum(t *testing.T) {
	f := &fakeJellyfin{checksum: "00000000000000000000000000000000"}
	jf, admin := setup(t, f)
	_, err := Install(context.Background(), jf, admin, func(string) {})
	var p Problem
	if !errors.As(err, &p) || !strings.Contains(err.Error(), "checksum") || len(f.installed) != 0 {
		t.Errorf("err %v, installed %v", err, f.installed)
	}
}

func TestInstaller(t *testing.T) {
	f := &fakeJellyfin{checksum: Checksum}
	jf, admin := setup(t, f)
	have := false
	in := &Installer{Jellyfin: func() (*jellyfin.Client, jellyfin.Auth, bool) { return jf, admin, have },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := in.Start(); err == nil {
		t.Error("started without a Jellyfin")
	}
	have = true
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for in.Progress().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p := in.Progress(); !p.Done || p.Error != "" || p.Status.State != "Active" {
		t.Errorf("progress: %+v", p)
	}
}
