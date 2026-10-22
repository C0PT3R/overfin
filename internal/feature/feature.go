// Package feature is the internal contract between Overfin's core and some
// of its features (the watchlist, invitations, migration).
//
// The core is the server and its guards, identity, sessions, accounts and
// profiles, and access to the media backend as a profile. A feature written
// on this contract adds to it through what this package offers, and
// nothing else: routes that act as the calling profile, the backend as that
// profile only, its own tables in its own database, and errors in the
// API's standard form. That keeps each one isolated, so it can't reach
// another feature, the media server or the core's data.
//
// Every feature is built in: there's no way to add one of one's own.
package feature

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
)

// Module is one feature grafted onto the core.
type Feature interface {
	// Info describes the module. The core reads it once, at startup.
	Info() Info
	// Setup registers the module's routes. It runs each time the module is
	// switched on or the configuration changes, so it must only register,
	// never start work of its own.
	Setup(h Host) error
}

// Info describes a module.
type Info struct {
	// Name is how operators switch it on, e.g. "watchlist". Lowercase
	// letters, digits and dashes.
	Name string
	// Needs lists modules that must be switched on with it.
	Needs []string
	// Description is shown in the control panel.
	Description string
	// AdminUI, when set, is the module's tab in the control panel: a
	// panel.js whose default export is render(root, panel), called with
	// the tab's element and helpers (panel.api(method, path, body) for the
	// module's admin routes, panel.h for building elements). It's served
	// on the panel's private listener only, beside any files it needs.
	AdminUI fs.FS
	// Settings are the module's settings, shown with it in the control
	// panel; Host.Setting reads them.
	Settings []Setting
	// Migrations holds the module's own SQL migrations, named 001_….sql,
	// 002_….sql and so on, applied in order and only once, to the module's
	// own database. Tables start with the module's name. The core's tables
	// aren't there: rows belonging to a profile or an account are deleted
	// with it through Forgetter.
	Migrations fs.FS
}

// Host is what the core offers a module during Setup.
type Host interface {
	// Handle registers a route that acts as the calling profile. The
	// pattern is "METHOD /api/v1/…" in Go's ServeMux syntax; catch-all
	// patterns are refused. Before h runs, the core has checked the login,
	// found the account and the active profile (PIN, expiry, group…), and
	// answered the caller itself if anything was missing.
	Handle(pattern string, h Handler) error
	// HandleVisitor registers a route for visitors: people signed in at the
	// identity provider who have no account here yet. The pattern is like
	// Handle's. Before h runs, the core has checked the login, that the
	// visitor may have an account (required group…), and that they have
	// none yet.
	HandleVisitor(pattern string, h VisitorHandler) error
	// HandleLink registers a link people open from elsewhere, an email or a
	// message, with no login: "GET /api/v1/…", a fixed path, acting on its
	// query only. It's the one kind of route other sites may send people
	// to, so it must reveal nothing, and only lead them on (setting a
	// cookie, redirecting).
	HandleLink(pattern string, h http.HandlerFunc) error
	// HandleAdmin registers a control panel route, for the module's tab.
	// The pattern is "METHOD /path"; the core files it under
	// /admin/api/modules/<name>/path, on the panel's private listener, where
	// only signed-in admins reach it.
	HandleAdmin(pattern string, h http.HandlerFunc) error
	// Setting is the current value of one of the module's settings: the
	// chosen value, or "on" or "off" for a switch.
	Setting(key string) string
	// Adoption is the media backend's side of bringing existing accounts
	// over, or nil when it can't.
	Adoption() Adoption
	// Templates lists the account templates, Default first.
	Templates(ctx context.Context) ([]AccountTemplate, error)
	// SiteURL is the site's address, e.g. "https://example.org", for links
	// to it; empty when the core doesn't know it.
	SiteURL() string
	// SignUpLink is a link where one person can make their account at the
	// identity provider, valid for a day; empty when the provider has no
	// such links (people then sign up as it lets them).
	SignUpLink(ctx context.Context) (string, error)
	// DB is the module's own database: its tables, and nothing else. The
	// core's data is reached through the host, never directly.
	DB() *sql.DB
	// Logger is the module's logger. Never log tokens, passwords, PINs or
	// what people watch.
	Logger() *slog.Logger
}

// Handler serves one request for the calling profile.
type Handler func(c *Caller, w http.ResponseWriter, r *http.Request)

// Caller is who's making a request: the account and its active profile.
type Caller struct {
	AccountID string
	ProfileID string
	// Kind is the profile's kind: "standard" or "kids".
	Kind string
	// Owner reports whether this is the account's owner profile.
	Owner bool
	// Backend is the media backend, as this profile and only as it: the
	// profile's own limits (a kids rating ceiling, say) apply to everything
	// it returns.
	Backend Backend
}

// Backend is the media backend seen as one profile.
type Backend interface {
	// Title returns one title (in a library or not), or ErrNotFound when it
	// doesn't exist or this profile may not see it.
	Title(ctx context.Context, id string) (Title, error)
	// Titles returns the titles among ids this profile can see, in no
	// particular order; the others are left out.
	Titles(ctx context.Context, ids []string) ([]Title, error)
	// TitleOfItem is the title ID of a library item ("item_…"), for data
	// kept before titles had IDs of their own; "" when it's unknown.
	TitleOfItem(ctx context.Context, itemID string) (string, error)
}

// Forgetter is implemented by a module that keeps data about profiles or
// accounts: when the core deletes some, it tells every module that has
// them, switched on or not, and the module deletes what it holds about
// them (or, for what must stay counted, cuts the link). db is the module's
// own database. An error is logged; the deletion itself goes on.
type Forgetter interface {
	ForgetProfiles(ctx context.Context, db *sql.DB, profileIDs []string) error
	ForgetAccount(ctx context.Context, db *sql.DB, accountID string) error
}

// Exporter is implemented by a module that holds data about a profile, so
// "Download my data" includes it. Export returns something that encodes to
// JSON; it must never include secrets.
type Exporter interface {
	Export(ctx context.Context, h Host, c *Caller) (any, error)
}
