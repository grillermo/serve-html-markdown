package db

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"serve-html-markdown/db/migrations"
)

// migrateLockKey is the Postgres advisory lock key serializing migrators.
const migrateLockKey int64 = 7351840291

var migrationName = regexp.MustCompile(`^(\d{14})_[^/]+\.sql$`)

// Beginner is anything that can start a transaction, such as *pgxpool.Pool,
// *pgx.Conn or pgx.Tx.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Migrate applies the embedded migrations (db/migrations) that are not yet
// recorded in schema_migrations.
//
// Run it on a DIRECT Postgres connection (port 5432), never through PgBouncer
// in transaction mode: advisory locks and multi-statement DDL files need a
// stable session. This is the same reason ./serve runs db:migrate directly
// today.
func Migrate(ctx context.Context, db Beginner) error {
	return MigrateFS(ctx, db, migrations.FS)
}

// MigrateFS is Migrate over an arbitrary file system whose root holds
// <14-digit version>_<name>.sql files. Versions are recorded in Rails'
// schema_migrations table, so Rails ignores them. Each file runs in its own
// transaction; a failure rolls that file back and records nothing for it.
// Concurrent callers are serialized with a transaction-level advisory lock and
// re-check the version under it, so a file is applied exactly once.
func MigrateFS(ctx context.Context, db Beginner, fsys fs.FS) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	type migration struct{ version, file string }
	var list []migration
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			return fmt.Errorf("migration %q: file name must start with a 14-digit version (YYYYMMDDHHMMSS_name.sql)", name)
		}
		if other, dup := seen[m[1]]; dup {
			return fmt.Errorf("migrations %q and %q share version %s", other, name, m[1])
		}
		seen[m[1]] = name
		list = append(list, migration{m[1], name})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].version < list[j].version })

	for _, m := range list {
		sqlBytes, err := fs.ReadFile(fsys, m.file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.file, err)
		}
		if err := applyOne(ctx, db, m.version, string(sqlBytes)); err != nil {
			return fmt.Errorf("migration %s: %w", m.file, err)
		}
	}
	return nil
}

func applyOne(ctx context.Context, db Beginner, version, sql string) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	var applied bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&applied); err != nil {
		return fmt.Errorf("check version: %w", err)
	}
	if applied {
		return tx.Rollback(ctx)
	}
	if _, err = tx.Exec(ctx, sql); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
