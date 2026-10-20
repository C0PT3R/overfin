package overfin

import (
	"context"
	"fmt"
	"regexp"

	"github.com/C0PT3R/overfin/internal/config"
	"github.com/C0PT3R/overfin/internal/store"
	"github.com/C0PT3R/overfin/module"
)

// modules are the modules compiled into this build, given to Main.
var modules []module.Module

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
		var settings []config.Setting
		for _, s := range info.Settings {
			if !moduleName.MatchString(s.Key) {
				return fmt.Errorf("module %s: setting %q: lowercase letters, digits and dashes only", info.Name, s.Key)
			}
			cs := config.Setting{Key: s.Key, Label: s.Label, Hint: s.Hint}
			for _, c := range s.Choices {
				cs.Choices = append(cs.Choices, config.Choice{Value: c.Value, Label: c.Label})
			}
			settings = append(settings, cs)
		}
		config.RegisterPlugin(info.Name, info.Needs, info.Description, settings)
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
