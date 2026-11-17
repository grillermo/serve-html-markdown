package store

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExpansionModes are the allowed values of users.expansion_mode (and of
// expansions.mode); the column defaults to "create_new".
var ExpansionModes = []string{"create_new", "edit_in_place"}

// User is a row of users. Password hashes are bcrypt, written by Devise or by
// UpsertUser.
type User struct {
	ID                int64
	Email             string
	EncryptedPassword string
	ExpansionMode     string
	RememberCreatedAt *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const userColumns = `id, email, encrypted_password, expansion_mode, remember_created_at, created_at, updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.EncryptedPassword, &u.ExpansionMode, &u.RememberCreatedAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// Devise's Devise.email_regexp, with \A and \z as in Ruby.
var emailPattern = regexp.MustCompile(`\A[^@\s]+@[^@\s]+\z`)

// normalizeEmail applies Devise's strip_whitespace_keys and case_insensitive_keys.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// FindUserByEmail looks a user up the way Devise does: whitespace is stripped
// and the case ignored. It returns nil, nil when there is no such user.
func FindUserByEmail(ctx context.Context, db DB, email string) (*User, error) {
	if !validText(email) {
		return nil, nil
	}
	u, err := scanUser(db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, normalizeEmail(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// UpsertUser creates the user, or replaces the password hash of the existing
// one with that (normalized) email. The email must look like an address
// (ErrInvalidEmail otherwise). New users get the default expansion mode.
func UpsertUser(ctx context.Context, db DB, email, bcryptHash string) (*User, error) {
	// Check before normalizing: ToLower turns invalid UTF-8 into U+FFFD.
	if !validText(email) {
		return nil, ErrInvalidEmail
	}
	email = normalizeEmail(email)
	if !emailPattern.MatchString(email) {
		return nil, ErrInvalidEmail
	}
	now := utcNow()
	return scanUser(db.QueryRow(ctx, `INSERT INTO users (email, encrypted_password, created_at, updated_at)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (email) DO UPDATE SET encrypted_password = EXCLUDED.encrypted_password, updated_at = EXCLUDED.updated_at
		RETURNING `+userColumns, email, bcryptHash, now))
}

// FindUserByID returns the user, or nil, nil when there is none.
func FindUserByID(ctx context.Context, db DB, id int64) (*User, error) {
	u, err := scanUser(db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// SetExpansionMode stores the user's remembered expansion mode. Modes outside
// ExpansionModes give ErrInvalidExpansionMode; an unknown user gives ErrNotFound.
func SetExpansionMode(ctx context.Context, db DB, userID int64, mode string) error {
	if !slices.Contains(ExpansionModes, mode) {
		return ErrInvalidExpansionMode
	}
	tag, err := db.Exec(ctx, `UPDATE users SET expansion_mode = $1, updated_at = $2 WHERE id = $3`,
		mode, utcNow(), userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
