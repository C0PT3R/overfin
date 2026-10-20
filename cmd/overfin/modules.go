package main

import (
	"context"
	"fmt"
	"regexp"

	"github.com/C0PT3R/overfin/internal/config"
	"github.com/C0PT3R/overfin/internal/store"
	"github.com/C0PT3R/overfin/module"
	"github.com/C0PT3R/overfin/modules/watchlist"
)

// modules are the modules compiled into this build: the official ones. An
// operator's own build adds theirs to this list.
var modules = []module.Module{
	watchlist.Module{},
}

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// registerModules makes the modules known to the configuration, so
// operators can switch them on. It runs before the configuration loads.
func registerModules() error {
	for _, m := range modules {
		info := m.Info()
		if !moduleName.MatchString(info.Name) {
			return fmt.Errorf("module name %q: lowercase letters, digits and dashes only", info.Name)
		}
		if _, taken := config.KnownPlugins[info.Name]; taken {
			return fmt.Errorf("module %q: the name is already taken", info.Name)
		}
		config.RegisterPlugin(info.Name, info.Needs, info.Description)
	}
	return nil
}

// migrateModules applies each module's own migrations.
func migrateModules(ctx context.Context, db *store.Store) error {
	for _, m := range modules {
		info := m.Info()
		if err := db.MigrateModule(ctx, info.Name, info.Migrations); err != nil {
			return err
		}
	}
	return nil
}
