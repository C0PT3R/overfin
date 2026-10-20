// Package module is the contract between Overfin's core and the modules
// grafted onto it.
//
// The core is the gateway: the server and its guards, identity, sessions,
// accounts and profiles, and access to the media backend as a profile. A
// module adds a feature on top (a watchlist, invites, a catalog…) through
// what this package offers, and nothing else: routes that act as the
// calling profile, the backend as that profile only, its own tables, and
// errors in the API's standard form.
//
// Modules are compiled in. The official build includes the official
// modules; an operator who wants their own builds Overfin with a few-line
// main package that adds it. Nothing is loaded while running.
//
// Official modules carry Overfin's guarantees. A module runs inside Overfin
// with its full power: this contract keeps a well-behaved module safe (the
// core refuses catch-all routes, requires a login, and hands a module only
// the calling profile's access to the backend), but it's a convention, not
// a sandbox. A third-party module is its developer's responsibility, and the
// operator's for running it, as with any program.
package module

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
)

// Module is one feature grafted onto the core.
type Module interface {
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
	// 002_….sql and so on, applied in order and only once. Tables should
	// start with the module's name, and rows belonging to a profile should
	// reference profiles(id) ON DELETE CASCADE, so they go with it.
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
	// DB is the database, for the module's own tables.
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
	// Item returns one title, or ErrNotFound when it doesn't exist or this
	// profile may not see it.
	Item(ctx context.Context, id string) (Item, error)
	// Items returns the titles among ids this profile can see, in no
	// particular order; the others are left out.
	Items(ctx context.Context, ids []string) ([]Item, error)
}

// Exporter is implemented by a module that holds data about a profile, so
// "Download my data" includes it. Export returns something that encodes to
// JSON; it must never include secrets.
type Exporter interface {
	Export(ctx context.Context, h Host, c *Caller) (any, error)
}
