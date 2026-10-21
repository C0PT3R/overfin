package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/C0PT3R/overfin-server/internal/creation"
	"github.com/C0PT3R/overfin-server/internal/feature"
	"github.com/C0PT3R/overfin-server/internal/identity"
	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/library"
	"github.com/C0PT3R/overfin-server/internal/plugin"
	"github.com/C0PT3R/overfin-server/internal/problem"
	"github.com/C0PT3R/overfin-server/internal/provision"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// Mount grafts a module onto the core: its routes go through the same
// registrar, and so the same guards, as the core's own, and each request
// reaches it as the calling profile.
type Mount struct {
	Module  feature.Feature
	Library Library
	DB      *sql.DB
	Logger  *slog.Logger
	// Admin collects the modules' control panel routes; nil when there's
	// no panel.
	Admin *http.ServeMux
	// Settings are the module's settings, as the operator set them.
	Settings map[string]string
	// Peers are all the modules switched on, this one included: each has
	// its say when this one sets a visitor's account up.
	Peers []Mount
	// SiteURL is the site's address, when known.
	SiteURL string
}

func (m Mount) Name() string { return m.Module.Info().Name }

func (m Mount) Register(r *plugin.Registrar) error {
	return m.Module.Setup(&host{mount: m, reg: r})
}

// host is the core's side of feature.Host.
type host struct {
	mount Mount
	reg   *plugin.Registrar
}

func (h *host) DB() *sql.DB { return h.mount.DB }

func (h *host) Setting(key string) string { return h.mount.Settings[key] }

func (h *host) SiteURL() string { return h.mount.SiteURL }

func (h *host) Templates(ctx context.Context) ([]feature.AccountTemplate, error) {
	ts, err := h.mount.Library.Provisioner.Store.Templates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]feature.AccountTemplate, len(ts))
	for i, t := range ts {
		out[i] = feature.AccountTemplate{ID: t.ID, Name: t.Name, MaxStreams: t.MaxStreams, MaxProfiles: t.MaxProfiles}
	}
	return out, nil
}

func (h *host) HandleLink(pattern string, fn http.HandlerFunc) error {
	return h.reg.HandleNavigation(pattern, fn)
}

// Adoption is Jellyfin's: it can bring its existing users over.
func (h *host) Adoption() feature.Adoption { return adoption{h.mount.Library.Provisioner} }

type adoption struct{ prov *provision.Provisioner }

func (a adoption) Linked(ctx context.Context) (int, error) {
	linked, err := a.prov.Store.LinkedProfiles(ctx)
	return len(linked), err
}

func (a adoption) MoveLinked(ctx context.Context) (int, error) { return a.prov.MoveLinked(ctx) }

func (h *host) Logger() *slog.Logger {
	return h.mount.Logger.With("module", h.mount.Module.Info().Name)
}

// adminPattern checks a module's admin route and files it under the
// module's own prefix.
var adminPattern = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) (/[A-Za-z0-9_./{}-]*)$`)

func (h *host) HandleAdmin(pattern string, fn http.HandlerFunc) error {
	m := adminPattern.FindStringSubmatch(pattern)
	name := h.mount.Module.Info().Name
	if m == nil || strings.Contains(m[2], "...}") || strings.HasSuffix(m[2], "/") {
		return fmt.Errorf("module %s: admin route %q: must be \"METHOD /path\", without catch-alls", name, pattern)
	}
	if h.mount.Admin == nil {
		return nil // no control panel on this instance
	}
	h.mount.Admin.HandleFunc(m[1]+" /admin/api/modules/"+name+m[2], fn)
	return nil
}

func (h *host) Handle(pattern string, fn feature.Handler) error {
	l := h.mount.Library
	return h.reg.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		c := l.Sessions.Current(w, r)
		if c == nil {
			return // the core already answered: no login, no profile…
		}
		fn(CallerFor(l, c), w, r)
	})
}

func (h *host) HandleVisitor(pattern string, fn feature.VisitorHandler) error {
	l := h.mount.Library
	return h.reg.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		id, _ := identity.FromContext(r.Context()) // the registrar guarantees one
		_, _, err := l.Provisioner.Find(r.Context(), id)
		switch {
		case err == nil:
			problem.Send(w, problem.AccountExists, "")
		case !errors.Is(err, provision.ErrNoAccount):
			accountResult(w, l.Sessions, l.Logger, nil, nil, err, 0)
		default:
			fn(visitor{mount: h.mount, r: r}, w, r)
		}
	})
}

// visitor is the core's side of feature.Visitor.
type visitor struct {
	mount Mount
	r     *http.Request
}

func (v visitor) Adopt(w http.ResponseWriter, username, password string, keepLogin bool) bool {
	if username == "" || len(username) > 128 || password == "" || len(password) > 256 {
		problem.Send(w, problem.InvalidRequest, "")
		return false
	}
	l := v.mount.Library
	id, _ := identity.FromContext(v.r.Context())
	acct, prof, err := l.Provisioner.Claim(creation.Adopting(v.r.Context()), id, username, password, !keepLogin,
		creationHooks(v.r, v.mount.Peers, l.Provisioner))
	accountResult(w, l.Sessions, l.Logger, acct, prof, err, http.StatusCreated)
	return err == nil
}

// offerName is a setup offer's member name in account-setup-required.
var offerName = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,31}$`)

// SetupOffers gathers what the modules offer visitors besides a new
// account, as account-setup-required carries it.
func SetupOffers(mounts []Mount) (map[string]string, error) {
	offers := map[string]string{}
	for _, m := range mounts {
		so, ok := m.Module.(feature.SetupOffer)
		if !ok {
			continue
		}
		name, value, ok := so.Offer(&host{mount: m})
		if !ok {
			continue
		}
		if !offerName.MatchString(name) || slices.Contains(problem.Members, name) {
			return nil, fmt.Errorf("module %s: setup offer %q: not a usable name", m.Name(), name)
		}
		if _, taken := offers[name]; taken {
			return nil, fmt.Errorf("module %s: setup offer %q: another module offers it", m.Name(), name)
		}
		offers[name] = value
	}
	return offers, nil
}

// CallerFor is the module's view of a caller.
func CallerFor(l Library, c *Caller) *feature.Caller {
	return &feature.Caller{
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

func (b profileBackend) Item(ctx context.Context, id string) (feature.Item, error) {
	jfID, ok := library.Unwrap(library.ItemPrefix, id)
	if !ok {
		return feature.Item{}, feature.ErrNotFound
	}
	raw, err := b.l.Jellyfin.Item(ctx, auth(b.p), b.p.JellyfinUserID, jfID)
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		return feature.Item{}, feature.ErrNotFound
	}
	if err != nil {
		return feature.Item{}, err
	}
	s, ok := library.Summary(raw)
	if !ok {
		return feature.Item{}, feature.ErrNotFound
	}
	return s, nil
}

func (b profileBackend) Items(ctx context.Context, ids []string) ([]feature.Item, error) {
	var jfIDs []string
	for _, id := range ids {
		if jfID, ok := library.Unwrap(library.ItemPrefix, id); ok {
			jfIDs = append(jfIDs, jfID)
		}
	}
	out := []feature.Item{}
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
	ex, ok := m.Module.(feature.Exporter)
	if !ok {
		return nil, false, nil
	}
	data, err = ex.Export(ctx, &host{mount: m}, CallerFor(m.Library, c))
	return data, true, err
}

// creationHooks gives the switched-on modules their say in creating an
// account for the visitor making r: each may refuse it or shape it, and is
// told once it exists. The first plan given shapes the account.
func creationHooks(r *http.Request, mounts []Mount, prov *provision.Provisioner) *provision.Hooks {
	type say struct {
		mount Mount
		gate  feature.AccountGate
		plan  *feature.AccountPlan
	}
	var said []say
	return &provision.Hooks{
		Before: func(ctx context.Context) error {
			for _, m := range mounts {
				g, ok := m.Module.(feature.AccountGate)
				if !ok {
					continue
				}
				plan, err := g.NewAccount(ctx, &host{mount: m}, r)
				if err != nil {
					return err
				}
				said = append(said, say{mount: m, gate: g, plan: plan})
			}
			return nil
		},
		After: func(ctx context.Context, acct *store.Account, owner *store.Profile) error {
			for _, s := range said {
				if s.plan != nil {
					if err := applyPlan(ctx, prov.Store, acct.ID, s.plan); err != nil {
						return err
					}
					break
				}
			}
			for _, s := range said {
				if err := s.gate.AccountCreated(ctx, &host{mount: s.mount}, r, acct.ID, s.plan); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// applyPlan shapes a new account: its template, when its access ends, and
// its label.
func applyPlan(ctx context.Context, db *store.Store, accountID string, plan *feature.AccountPlan) error {
	if plan.Template != "" {
		if err := db.SetAccountTemplate(ctx, accountID, plan.Template); err != nil {
			return fmt.Errorf("plan: template %q: %w", plan.Template, err)
		}
	}
	if !plan.Expires.IsZero() {
		if err := db.SetAccountExpiry(ctx, accountID, plan.Expires); err != nil {
			return err
		}
	}
	if plan.Label != "" {
		if err := db.SetAccountLabel(ctx, accountID, plan.Label); err != nil {
			return err
		}
	}
	return nil
}
