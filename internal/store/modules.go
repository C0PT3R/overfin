package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Modules keep their data in databases of their own: a module only ever
// sees its own tables, and reaches the core's data through its host, never
// directly (the core has authority over modules).

// OpenModule opens (or creates) a module's own database at path, with the
// same settings as the core's, and applies its migrations (001_….sql,
// 002_….sql…) that haven't run yet, each in its own transaction.
func OpenModule(ctx context.Context, module, path string, migrations fs.FS) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=temp_store(2)")
	if err != nil {
		return nil, fmt.Errorf("store: module %s: %w", module, err)
	}
	db.SetMaxOpenConns(1)
	if err := migrateModule(ctx, db, module, migrations); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

var moduleMigrationName = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)

// migrateModule applies a module's migrations; the database's user_version
// counts those applied.
func migrateModule(ctx context.Context, db *sql.DB, module string, migrations fs.FS) error {
	if migrations == nil {
		return nil
	}
	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("store: module %s: reading its schema version: %w", module, err)
	}
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return fmt.Errorf("store: module %s migrations: %w", module, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i, name := range names {
		version := i + 1
		m := moduleMigrationName.FindStringSubmatch(name)
		if m == nil || m[1] != fmt.Sprintf("%03d", version) {
			return fmt.Errorf("store: module %s migration %s is misnamed or out of sequence", module, name)
		}
		if version <= current {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// PRAGMA can't take a parameter; version is a number we made.
		if _, err := tx.ExecContext(ctx, string(body)+fmt.Sprintf(";\nPRAGMA user_version = %d;", version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: module %s, applying %s: %w", module, name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// MoveModuleTables moves a module's tables from the core's database, where
// modules used to keep them, into its own database at path: once, for
// installs from before modules had databases of their own. A module's
// tables are the ones named after it ("watchlist", "invites_uses"). The
// module's own database must be migrated already; rows are copied column by
// column, then the old tables dropped. It returns how many tables moved.
func (s *Store) MoveModuleTables(ctx context.Context, module, path string) (int, error) {
	var ran bool
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM module_migrations WHERE module = ?`, module).Scan(&ran); err != nil {
		return 0, err
	}
	if !ran {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND (name = ? OR name LIKE ? ESCAPE '\')`, module, strings.ReplaceAll(module, "_", `\_`)+`\_%`)
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// One connection: ATTACH stays on it for the whole move.
	if _, err := s.db.ExecContext(ctx, `ATTACH DATABASE ? AS modulestore`, "file:"+path); err != nil {
		return 0, err
	}
	defer s.db.ExecContext(context.Background(), `DETACH DATABASE modulestore`)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// The module's own tables may reference each other: foreign keys are
	// checked at commit only, once every table is copied.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return 0, err
	}
	for _, table := range tables {
		cols, err := sharedColumns(ctx, tx, table)
		if err != nil {
			return 0, err
		}
		if len(cols) > 0 {
			list := `"` + strings.Join(cols, `", "`) + `"`
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT OR IGNORE INTO modulestore."%s" (%s) SELECT %s FROM main."%s"`,
				table, list, list, table)); err != nil {
				return 0, fmt.Errorf("store: moving %s: %w", table, err)
			}
		}
	}
	// Dropped children first, so no foreign key in the core's database
	// stands in the way.
	for _, table := range slices.Backward(tables) {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE main."%s"`, table)); err != nil {
			return 0, fmt.Errorf("store: dropping %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM module_migrations WHERE module = ?`, module); err != nil {
		return 0, err
	}
	return len(tables), tx.Commit()
}

// sharedColumns lists the columns table has in both the core's database and
// the module's.
func sharedColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	columns := func(schema string) ([]string, error) {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT name FROM %s.pragma_table_info(?)`, schema), table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			out = append(out, name)
		}
		return out, rows.Err()
	}
	old, err := columns("main")
	if err != nil {
		return nil, err
	}
	now, err := columns("modulestore")
	if err != nil {
		return nil, err
	}
	var shared []string
	for _, c := range old {
		if slices.Contains(now, c) {
			shared = append(shared, c)
		}
	}
	return shared, nil
}

// Forgetter is told when profiles and accounts are deleted, so data kept
// elsewhere (modules' own databases) goes with them.
type Forgetter interface {
	ForgetProfiles(ctx context.Context, profileIDs []string)
	ForgetAccount(ctx context.Context, accountID string)
}

// SetForgetter sets who's told of deletions. Set it before serving.
func (s *Store) SetForgetter(f Forgetter) { s.forget = f }

// forgotten tells the forgetter, if any, that profiles (and an account)
// are gone.
func (s *Store) forgotten(ctx context.Context, profileIDs []string, accountID string) {
	if s.forget == nil {
		return
	}
	if len(profileIDs) > 0 {
		s.forget.ForgetProfiles(ctx, profileIDs)
	}
	if accountID != "" {
		s.forget.ForgetAccount(ctx, accountID)
	}
}
