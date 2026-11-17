package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AllowedExtensions are the file extensions that are served and listed.
var AllowedExtensions = map[string]bool{".html": true, ".md": true, ".markdown": true}

func allowed(name string) bool { return AllowedExtensions[strings.ToLower(filepath.Ext(name))] }

// ServedFile is a row of served_files.
type ServedFile struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const servedFileColumns = `id, name, created_at, updated_at`

func scanServedFile(row pgx.Row) (ServedFile, error) {
	var f ServedFile
	err := row.Scan(&f.ID, &f.Name, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

// EscapeLike escapes the LIKE wildcards (and the escape character itself) so s
// matches literally in a pattern used with ESCAPE '\'.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// RecordServedFile inserts name unless it already exists (created_at stays
// frozen). Names without an allowed extension are ignored.
func RecordServedFile(ctx context.Context, db DB, name string) error {
	if !allowed(name) {
		return nil
	}
	now := time.Now().UTC()
	_, err := db.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)
		ON CONFLICT (name) DO NOTHING`, name, now)
	return err
}

// RemoveServedFile deletes the row for name, if any.
func RemoveServedFile(ctx context.Context, db DB, name string) error {
	_, err := db.Exec(ctx, `DELETE FROM served_files WHERE name = $1`, name)
	return err
}

// RecordServedFileModification bumps updated_at of name, creating the row when
// absent. Like the Rails original it does not check the extension.
func RecordServedFileModification(ctx context.Context, db DB, name string) error {
	now := time.Now().UTC()
	_, err := db.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)
		ON CONFLICT (name) DO UPDATE SET updated_at = EXCLUDED.updated_at`, name, now)
	return err
}

// NewestServedFile returns the most recently added row (ties broken by id), or
// nil when the table is empty.
func NewestServedFile(ctx context.Context, db DB) (*ServedFile, error) {
	f, err := scanServedFile(db.QueryRow(ctx, `SELECT `+servedFileColumns+`
		FROM served_files ORDER BY created_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ServedFilesByUpdated lists every row, most recently updated first.
func ServedFilesByUpdated(ctx context.Context, db DB) ([]ServedFile, error) {
	rows, err := db.Query(ctx, `SELECT `+servedFileColumns+` FROM served_files ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ServedFile, error) { return scanServedFile(row) })
}

// ServedFileNames returns, sorted by name, the names equal to exact or matching
// likePattern (a LIKE pattern using backslash as the escape character; see
// EscapeLike).
func ServedFileNames(ctx context.Context, db DB, likePattern, exact string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT name FROM served_files
		WHERE name = $1 OR name LIKE $2 ESCAPE '\' ORDER BY name`, exact, likePattern)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SyncServedFiles makes served_files mirror the regular files with an allowed
// extension directly inside dir: missing rows are inserted (existing rows keep
// their timestamps) and rows without a file are deleted. Subdirectories and
// files with other extensions (including ".<name>.expansion.lock" files) are
// ignored.
func SyncServedFiles(ctx context.Context, db DB, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := []string{} // non-nil: an empty array must delete every row
	for _, e := range entries {
		if !allowed(e.Name()) {
			continue
		}
		// os.Stat follows symlinks, like Ruby's Pathname#file?.
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) > 0 {
		now := time.Now().UTC()
		if _, err := db.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at)
			SELECT n, $2::timestamp, $2::timestamp FROM unnest($1::text[]) AS n
			ON CONFLICT (name) DO NOTHING`, names, now); err != nil {
			return err
		}
	}
	_, err = db.Exec(ctx, `DELETE FROM served_files WHERE NOT (name = ANY($1::text[]))`, names)
	return err
}
