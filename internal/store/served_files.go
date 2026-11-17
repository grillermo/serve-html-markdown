package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AllowedExtensions are the file extensions that are served and listed.
var AllowedExtensions = map[string]bool{".html": true, ".md": true, ".markdown": true}

// allowed mirrors Ruby's File.extname: leading dots are not an extension
// separator, so ".md" and "..md" have none while ".x.md" has ".md". Names that
// Postgres could not store are never allowed.
func allowed(name string) bool { return validText(name) && hasAllowedExt(name) }

func hasAllowedExt(name string) bool { return HasAllowedExtension(name) }

// Ext mirrors Ruby's File.extname on a base name: leading dots are skipped, a
// trailing dot yields ".", and the result keeps its case.
func Ext(name string) string {
	return filepath.Ext(strings.TrimLeft(name, "."))
}

// HasAllowedExtension reports whether name's extension (Ext, case-insensitive)
// is one of AllowedExtensions. It only looks at the extension; use it on a
// base name.
func HasAllowedExtension(name string) bool {
	return AllowedExtensions[strings.ToLower(Ext(name))]
}

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
	now := utcNow()
	_, err := db.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)
		ON CONFLICT (name) DO NOTHING`, name, now)
	return err
}

// RemoveServedFile deletes the row for name, if any.
func RemoveServedFile(ctx context.Context, db DB, name string) error {
	if !validText(name) {
		return nil
	}
	_, err := db.Exec(ctx, `DELETE FROM served_files WHERE name = $1`, name)
	return err
}

// RecordServedFileModification bumps updated_at of name, creating the row when
// absent. Like the Rails original it does not check the extension (names with
// NUL bytes or invalid UTF-8 are ignored).
func RecordServedFileModification(ctx context.Context, db DB, name string) error {
	if !validText(name) {
		return nil
	}
	now := utcNow()
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
// ignored, and so are names that are not valid UTF-8 (logged).
//
// It is not atomic: the directory listing, the insert and the prune are
// separate statements. Callers that need atomicity can pass a pgx.Tx. A row
// recorded after the sync started (created_at at or after the start of the
// call) is never pruned, so a file added during the sync is not lost.
func SyncServedFiles(ctx context.Context, db DB, dir string) error {
	start := utcNow()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := []string{} // non-nil: an empty array must delete every row
	for _, e := range entries {
		if !allowed(e.Name()) {
			if hasAllowedExt(e.Name()) {
				slog.Warn("served_files: skipping file name that is not valid UTF-8", "name", fmt.Sprintf("%q", e.Name()))
			}
			continue
		}
		regular := e.Type().IsRegular()
		if e.Type()&fs.ModeSymlink != 0 {
			// Follow symlinks, like Ruby's Pathname#file?.
			info, err := os.Stat(filepath.Join(dir, e.Name()))
			regular = err == nil && info.Mode().IsRegular()
		}
		if regular {
			names = append(names, e.Name())
		}
	}
	if len(names) > 0 {
		now := utcNow()
		if _, err := db.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at)
			SELECT n, $2::timestamp, $2::timestamp FROM unnest($1::text[]) AS n
			ON CONFLICT (name) DO NOTHING`, names, now); err != nil {
			return err
		}
	}
	_, err = db.Exec(ctx, `DELETE FROM served_files WHERE NOT (name = ANY($1::text[])) AND created_at < $2::timestamp`, names, start)
	return err
}
