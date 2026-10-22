package overfin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/invites"
	"github.com/overfin/server/internal/migration"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/watchlist"
)

// modules are Overfin's features built on the internal feature contract,
// each in a package of its own, with its own database.
var modules = []feature.Feature{
	invites.Feature{},
	migration.Feature{},
	watchlist.Feature{},
}

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// The features are built in, so they're known to the configuration from the
// start, before anything loads it. A mistake in their names is Overfin's
// own, caught by any test.
func init() {
	if err := registerModules(); err != nil {
		panic(err)
	}
}

// registerModules makes the features known to the configuration, so
// operators can switch them on.
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

// openModules opens each module's own database, in a modules folder next
// to the core's, migrated, and with its tables moved there from the core's
// database where modules used to keep them. The core then tells modules of
// deleted profiles and accounts. The caller closes the databases.
func openModules(ctx context.Context, db *store.Store, storagePath string, logger *slog.Logger) (map[string]*sql.DB, error) {
	dir := filepath.Join(filepath.Dir(storagePath), "modules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dbs := map[string]*sql.DB{}
	for _, m := range modules {
		info := m.Info()
		path := filepath.Join(dir, info.Name+".db")
		mdb, err := store.OpenModule(ctx, info.Name, path, info.Migrations)
		if err != nil {
			closeModules(dbs)
			return nil, err
		}
		dbs[info.Name] = mdb
		moved, err := db.MoveModuleTables(ctx, info.Name, path)
		if err != nil {
			closeModules(dbs)
			return nil, fmt.Errorf("module %s: moving its tables to its own database: %w", info.Name, err)
		}
		if moved > 0 {
			logger.Info("a module's tables moved to its own database", "module", info.Name, "tables", moved)
		}
	}
	db.SetForgetter(forgetter{dbs: dbs, logger: logger})
	return dbs, nil
}

func closeModules(dbs map[string]*sql.DB) {
	for _, mdb := range dbs {
		mdb.Close()
	}
}

// forgetter tells every module that keeps data about profiles or accounts,
// switched on or not, that some are gone.
type forgetter struct {
	dbs    map[string]*sql.DB
	logger *slog.Logger
}

func (f forgetter) ForgetProfiles(ctx context.Context, profileIDs []string) {
	for _, m := range modules {
		if mf, ok := m.(feature.Forgetter); ok {
			if err := mf.ForgetProfiles(ctx, f.dbs[m.Info().Name], profileIDs); err != nil {
				f.logger.Error("a module couldn't forget deleted profiles", "module", m.Info().Name, "err", err)
			}
		}
	}
}

func (f forgetter) ForgetAccount(ctx context.Context, accountID string) {
	for _, m := range modules {
		if mf, ok := m.(feature.Forgetter); ok {
			if err := mf.ForgetAccount(ctx, f.dbs[m.Info().Name], accountID); err != nil {
				f.logger.Error("a module couldn't forget a deleted account", "module", m.Info().Name, "err", err)
			}
		}
	}
}
