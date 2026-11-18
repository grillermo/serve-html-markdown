package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ExpansionStatuses are the allowed values of expansions.status, in the order
// of a job's life. New rows start "pending". The modes are ExpansionModes.
var ExpansionStatuses = []string{"pending", "processing", "completed", "failed"}

// InvalidExpansionError reports which field CreateExpansion rejected. Message
// is Rails' wording ("can't be blank", "is not included in the list", "must be
// greater than or equal to 0", ...). Only the first problem, in attribute
// order (file_name, selected_text, question, occurrence, mode), is reported.
type InvalidExpansionError struct {
	Field   string // column name: "file_name", "mode", ...
	Message string
}

func (e *InvalidExpansionError) Error() string {
	return ErrInvalidExpansion.Error() + ": " + e.Field + " " + e.Message
}

// Is makes errors.Is(err, ErrInvalidExpansion) true, and, for a bad mode,
// errors.Is(err, ErrInvalidExpansionMode).
func (e *InvalidExpansionError) Is(target error) bool {
	return target == ErrInvalidExpansion || (e.Field == "mode" && target == ErrInvalidExpansionMode)
}

// Expansion is a row of expansions. Nullable columns are pointers (nil is
// NULL). Timings maps a stage name to epoch milliseconds (see StampExpansion).
type Expansion struct {
	ID             int64
	UserID         int64
	FileName       string
	SelectedText   string
	Question       string
	Occurrence     int
	Mode           string
	Status         string
	URL            *string
	ErrorDetail    *string
	FallbackAnchor *string
	ProviderUsed   *string
	HTMLBytes      *int
	UseOpenAI      bool
	Timings        map[string]int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// EditInPlace reports whether the expansion rewrites the file (as a new
// version) instead of linking a new page.
func (e *Expansion) EditInPlace() bool { return e.Mode == "edit_in_place" }

const expansionColumns = `id, user_id, file_name, selected_text, question, occurrence, mode, status, url,
	error_detail, fallback_anchor, provider_used, html_bytes, use_openai, timings, created_at, updated_at`

func scanExpansion(row pgx.Row) (*Expansion, error) {
	var e Expansion
	var timings string
	err := row.Scan(&e.ID, &e.UserID, &e.FileName, &e.SelectedText, &e.Question, &e.Occurrence, &e.Mode, &e.Status,
		&e.URL, &e.ErrorDetail, &e.FallbackAnchor, &e.ProviderUsed, &e.HTMLBytes, &e.UseOpenAI, &timings,
		&e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	e.Timings = parseTimings(timings)
	return &e, nil
}

// parseTimings reads the JSON text of expansions.timings, which Rails stored as
// an object of stage => epoch milliseconds. It keeps every key whose value is a
// JSON number (floats are truncated) and skips the rest; text that is not a JSON
// object (empty, null, an array, garbage) reads as empty.
func parseTimings(text string) map[string]int64 {
	m := map[string]int64{}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return m
	}
	for stage, value := range raw {
		if len(value) == 0 || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) {
			continue // not a number (a string, null, an object, ...)
		}
		var n json.Number
		if err := json.Unmarshal(value, &n); err != nil {
			continue
		}
		if i, err := n.Int64(); err == nil {
			m[stage] = i
		} else if f, err := n.Float64(); err == nil && f >= math.MinInt64 && f < math.MaxInt64 {
			m[stage] = int64(f)
		}
	}
	return m
}

// NewExpansion is what the controller submits. Mode "" means "create_new".
// A blank FallbackAnchor is stored as NULL. Status is always "pending" and
// timings start empty.
type NewExpansion struct {
	UserID         int64
	FileName       string
	SelectedText   string
	Question       string
	Occurrence     int
	Mode           string
	FallbackAnchor string
	UseOpenAI      bool
}

func (n NewExpansion) validate() *InvalidExpansionError {
	for _, f := range []struct{ field, value string }{
		{"file_name", n.FileName}, {"selected_text", n.SelectedText}, {"question", n.Question},
	} {
		if blank(f.value) {
			return &InvalidExpansionError{f.field, "can't be blank"}
		}
		if !validText(f.value) {
			return &InvalidExpansionError{f.field, "is invalid"}
		}
	}
	if n.Occurrence < 0 {
		return &InvalidExpansionError{"occurrence", "must be greater than or equal to 0"}
	}
	if n.Occurrence > math.MaxInt32 {
		return &InvalidExpansionError{"occurrence", "must be less than or equal to 2147483647"}
	}
	if !slices.Contains(ExpansionModes, n.Mode) {
		return &InvalidExpansionError{"mode", "is not included in the list"}
	}
	if !validText(n.FallbackAnchor) {
		return &InvalidExpansionError{"fallback_anchor", "is invalid"}
	}
	return nil
}

// CreateExpansion inserts a pending expansion and returns it. Invalid input
// gives an *InvalidExpansionError (errors.Is ErrInvalidExpansion); an unknown
// user gives ErrNotFound.
func CreateExpansion(ctx context.Context, db DB, n NewExpansion) (*Expansion, error) {
	if n.Mode == "" {
		n.Mode = "create_new"
	}
	if err := n.validate(); err != nil {
		return nil, err
	}
	var fallback *string
	if n.FallbackAnchor != "" {
		fallback = &n.FallbackAnchor
	}
	now := utcNow()
	e, err := scanExpansion(db.QueryRow(ctx, `INSERT INTO expansions
		(user_id, file_name, selected_text, question, occurrence, mode, fallback_anchor, use_openai, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		RETURNING `+expansionColumns,
		n.UserID, n.FileName, n.SelectedText, n.Question, n.Occurrence, n.Mode, fallback, n.UseOpenAI, now))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation: no such user
		return nil, ErrNotFound
	}
	return e, err
}

// FindExpansion returns the expansion, or nil, nil when there is none.
func FindExpansion(ctx context.Context, db DB, id int64) (*Expansion, error) {
	return nilIfNoRows(scanExpansion(db.QueryRow(ctx, `SELECT `+expansionColumns+` FROM expansions WHERE id = $1`, id)))
}

// FindUserExpansion is FindExpansion limited to the user's own rows: another
// user's expansion is nil, nil.
func FindUserExpansion(ctx context.Context, db DB, userID, id int64) (*Expansion, error) {
	return nilIfNoRows(scanExpansion(db.QueryRow(ctx,
		`SELECT `+expansionColumns+` FROM expansions WHERE id = $1 AND user_id = $2`, id, userID)))
}

// ClaimExpansion moves a pending expansion to "processing" and reports whether
// this caller did it: true once, false for every later or concurrent call (and
// for a missing row).
func ClaimExpansion(ctx context.Context, db DB, id int64) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE expansions SET status = 'processing', updated_at = $2
		WHERE id = $1 AND status = 'pending'`, id, utcNow())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// mergeTiming is the SQL that sets timings to the existing JSON object plus
// one key. Anything that is not a JSON object (empty text, null, an array)
// counts as empty. One statement, so Postgres' row lock serialises concurrent
// stamps and none is lost (Rails needed with_lock). $keyArg is the stage (text)
// and $valArg the epoch milliseconds (bigint).
func mergeTiming(keyArg, valArg string) string {
	return `timings = (CASE WHEN jsonb_typeof(NULLIF(timings, '')::jsonb) = 'object' THEN timings::jsonb ELSE '{}'::jsonb END
		|| jsonb_build_object(` + keyArg + `::text, ` + valArg + `::bigint))::text`
}

// ExpansionFailedDetail is the generic detail of a failed expansion: what the
// reader sees when the cause is not something they can act on.
const ExpansionFailedDetail = "Expansion failed."

// ErrExpansionFinished is returned by the terminal writes (CompleteExpansion,
// FailExpansion, SetExpansionURL) when the row exists but is already completed
// or failed: they never overwrite a finished expansion. It is a no-op, not a
// failure, for callers that were merely late.
var ErrExpansionFinished = errors.New("store: expansion already finished")

// openExpansion is the guard of every write that must not touch a finished row.
const openExpansion = `status IN ('pending', 'processing')`

// guardedWrite turns a guarded UPDATE that touched no row into
// ErrExpansionFinished when the row exists and ErrNotFound when it does not.
func guardedWrite(ctx context.Context, db DB, id int64, tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM expansions WHERE id = $1)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrExpansionFinished
	}
	return ErrNotFound
}

// CompleteExpansion records the generated page: status "completed", the url and
// the error cleared; ErrNotFound for a missing row and ErrExpansionFinished for
// one that is already completed or failed (a late writer never overwrites the
// outcome). The "completed" stage is then stamped in a second statement whose
// failure is only logged (as in Ruby, where stamp! rescues): a timings column
// that holds invalid JSON must not lose the result.
func CompleteExpansion(ctx context.Context, db DB, id int64, url string) error {
	if !validText(url) {
		return &InvalidExpansionError{"url", "is invalid"}
	}
	tag, err := db.Exec(ctx, `UPDATE expansions SET status = 'completed', url = $2, error_detail = NULL, updated_at = $3
		WHERE id = $1 AND `+openExpansion, id, url, utcNow())
	if err := guardedWrite(ctx, db, id, tag, err); err != nil {
		return err
	}
	StampExpansion(ctx, db, id, "completed", NowMS())
	return nil
}

// FailExpansion records a failure: status "failed", the url cleared and the
// detail stored. It is the last resort of the job's rescue block, so it never
// rejects odd text: NUL bytes are dropped and invalid UTF-8 replaced (see
// sanitizeDetail). ErrNotFound for a missing row, ErrExpansionFinished for one
// that is already completed or failed.
func FailExpansion(ctx context.Context, db DB, id int64, detail string) error {
	detail = sanitizeDetail(detail)
	tag, err := db.Exec(ctx, `UPDATE expansions
		SET status = 'failed', url = NULL, error_detail = $2, updated_at = $3
		WHERE id = $1 AND `+openExpansion, id, detail, utcNow())
	return guardedWrite(ctx, db, id, tag, err)
}

// SetExpansionURL durably records the url of a result whose files are already
// written, without changing the status: if the job dies before it completes the
// expansion, its re-run completes it with this url instead of generating again.
// ErrNotFound / ErrExpansionFinished as for CompleteExpansion.
func SetExpansionURL(ctx context.Context, db DB, id int64, url string) error {
	if !validText(url) {
		return &InvalidExpansionError{"url", "is invalid"}
	}
	tag, err := db.Exec(ctx, `UPDATE expansions SET url = $2, updated_at = $3
		WHERE id = $1 AND `+openExpansion, id, url, utcNow())
	return guardedWrite(ctx, db, id, tag, err)
}

// FailStaleExpansions finishes the expansions that no job is working on any
// more: pending or processing rows not touched (every stage stamp refreshes
// updated_at) for staleAfter. They come from outages that kept the job from
// claiming or from recording its outcome, and from crashes. A row that already
// has a url (its files were written, only the completion was lost) completes
// with it; the others fail with ExpansionFailedDetail. One statement, guarded
// by status, so a row finished meanwhile is left alone. It returns how many
// rows it finished.
func FailStaleExpansions(ctx context.Context, db DB, staleAfter time.Duration) (int64, error) {
	now := utcNow()
	tag, err := db.Exec(ctx, `UPDATE expansions SET
		status = CASE WHEN COALESCE(url, '') <> '' THEN 'completed' ELSE 'failed' END,
		error_detail = CASE WHEN COALESCE(url, '') <> '' THEN NULL ELSE $2 END,
		updated_at = $1
		WHERE `+openExpansion+` AND updated_at < $3`, now, ExpansionFailedDetail, now.Add(-staleAfter))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// StampExpansion records when a stage happened (epoch milliseconds, see NowMS)
// in the timings JSON without losing other stages; stamping a stage again
// replaces its time. Failures are logged and never returned: timings are
// diagnostics and must not break the request or job being measured.
func StampExpansion(ctx context.Context, db DB, id int64, stage string, epochMS int64) {
	if !validText(stage) {
		slog.Error("[Expansion] stamp failed", "err", "stage is not valid text")
		return
	}
	_, err := db.Exec(ctx, `UPDATE expansions SET `+mergeTiming(`$2`, `$3`)+`, updated_at = $4
		WHERE id = $1`, id, stage, epochMS, utcNow())
	if err != nil {
		slog.Error("[Expansion] stamp failed", "stage", stage, "err", err)
	}
}

// RecordExpansionResponse stores which LLM answered and how big its page was.
// Like Rails' update_columns it leaves updated_at alone. ErrNotFound for a
// missing row.
func RecordExpansionResponse(ctx context.Context, db DB, id int64, provider string, htmlBytes int) error {
	if !validText(provider) {
		return &InvalidExpansionError{"provider_used", "is invalid"}
	}
	return requireRow(db.Exec(ctx, `UPDATE expansions SET provider_used = $2, html_bytes = $3 WHERE id = $1`,
		id, provider, htmlBytes))
}

// ReleaseExpansion puts a "processing" expansion back to "pending" and reports
// whether it did. The job handler uses it when a shutdown cut the generation
// short, so the job's next run can claim the expansion again. Rows in any other
// status are left alone.
func ReleaseExpansion(ctx context.Context, db DB, id int64) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE expansions SET status = 'pending', updated_at = $2
		WHERE id = $1 AND status = 'processing'`, id, utcNow())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReclaimExpansion puts a "processing" expansion back to "pending" when it was
// last touched more than staleAfter ago (every stage stamp refreshes
// updated_at), and reports whether it did. It recovers an expansion whose job
// run died without finishing (a crash, a kill -9): the queue hands such a job
// out again only after its own lock went stale, and a live run keeps stamping,
// so a row this old has no live owner. Callers must pass a staleAfter well
// below the queue's stale window and above the longest quiet gap between two
// stamps of a healthy run. Trade-off: a healthy run that is silent for longer
// than staleAfter and is somehow re-run concurrently would be taken over; the
// queue's exclusive lock prevents that concurrency, and a take-over only
// repeats an idempotent generation.
func ReclaimExpansion(ctx context.Context, db DB, id int64, staleAfter time.Duration) (bool, error) {
	now := utcNow()
	tag, err := db.Exec(ctx, `UPDATE expansions SET status = 'pending', updated_at = $2
		WHERE id = $1 AND status = 'processing' AND updated_at < $3`, id, now, now.Add(-staleAfter))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
