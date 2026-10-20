package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/library"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/store"
	"github.com/C0PT3R/overfin/module"
)

// Mount grafts a module onto the core: its routes go through the same
// registrar, and so the same guards, as the core's own, and each request
// reaches it as the calling profile.
type Mount struct {
	Module  module.Module
	Library Library
	DB      *sql.DB
	Logger  *slog.Logger
}

func (m Mount) Name() string { return m.Module.Info().Name }

func (m Mount) Register(r *plugin.Registrar) error {
	return m.Module.Setup(&host{mount: m, reg: r})
}

// host is the core's side of module.Host.
type host struct {
	mount Mount
	reg   *plugin.Registrar
}

func (h *host) DB() *sql.DB { return h.mount.DB }

func (h *host) Logger() *slog.Logger {
	return h.mount.Logger.With("module", h.mount.Module.Info().Name)
}

func (h *host) Handle(pattern string, fn module.Handler) error {
	l := h.mount.Library
	return h.reg.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		c := l.Sessions.Current(w, r)
		if c == nil {
			return // the core already answered: no login, no profile…
		}
		fn(CallerFor(l, c), w, r)
	})
}

// CallerFor is the module's view of a caller.
func CallerFor(l Library, c *Caller) *module.Caller {
	return &module.Caller{
		AccountID: c.Account.ID, ProfileID: c.Profile.ID, Kind: c.Profile.Kind, Owner: c.IsOwner(),
		Backend: profileBackend{l: l, p: c.Profile},
	}
}

// profileBackend is the media backend as one profile, with that profile's
// own token: its limits apply to everything it returns.
type profileBackend struct {
	l Library
	p *store.Profile
}

func (b profileBackend) Item(ctx context.Context, id string) (module.Item, error) {
	jfID, ok := library.Unwrap(library.ItemPrefix, id)
	if !ok {
		return module.Item{}, module.ErrNotFound
	}
	raw, err := b.l.Jellyfin.Item(ctx, auth(b.p), b.p.JellyfinUserID, jfID)
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		return module.Item{}, module.ErrNotFound
	}
	if err != nil {
		return module.Item{}, err
	}
	s, ok := library.Summary(raw)
	if !ok {
		return module.Item{}, module.ErrNotFound
	}
	return s, nil
}

func (b profileBackend) Items(ctx context.Context, ids []string) ([]module.Item, error) {
	var jfIDs []string
	for _, id := range ids {
		if jfID, ok := library.Unwrap(library.ItemPrefix, id); ok {
			jfIDs = append(jfIDs, jfID)
		}
	}
	out := []module.Item{}
	for start := 0; start < len(jfIDs); start += 100 {
		raw, err := b.l.Jellyfin.ItemsByID(ctx, auth(b.p), b.p.JellyfinUserID, jfIDs[start:min(start+100, len(jfIDs))])
		if err != nil {
			return nil, err
		}
		for i := range raw {
			if s, ok := library.Summary(&raw[i]); ok {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// Export asks the module, if it holds data about profiles, for this
// caller's: ok is false when it holds none.
func (m Mount) Export(ctx context.Context, c *Caller) (data any, ok bool, err error) {
	ex, ok := m.Module.(module.Exporter)
	if !ok {
		return nil, false, nil
	}
	data, err = ex.Export(ctx, &host{mount: m}, CallerFor(m.Library, c))
	return data, true, err
}
