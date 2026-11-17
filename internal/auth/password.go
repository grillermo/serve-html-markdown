package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Cost is the bcrypt cost used for new hashes: config.stretches in config/initializers/devise.rb
// (12 outside the test env). Existing hashes of any cost verify, since the cost is stored in the hash.
const Cost = 12

// maxPasswordBytes is bcrypt's input limit. bcrypt-ruby (so Devise) silently ignores every byte
// after the 72nd, whereas x/crypto/bcrypt returns ErrPasswordTooLong. To keep accepting the
// passwords existing users already have, both functions below truncate like Ruby does.
const maxPasswordBytes = 72

func truncate(password string) []byte {
	b := []byte(password)
	if len(b) > maxPasswordBytes {
		b = b[:maxPasswordBytes]
	}
	return b
}

// CheckPassword reports whether password matches a Devise bcrypt hash. A blank or malformed hash
// never matches.
func CheckPassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), truncate(password)) == nil
}

// HashPassword returns a bcrypt hash at Cost, compatible with Devise (only the first 72 bytes of
// password count).
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword(truncate(password), Cost)
	return string(b), err
}

// SaltOf mirrors Devise's authenticatable_salt for database_authenticatable,
// encrypted_password[0,29]: the bcrypt prefix (version, cost and salt), so a password change
// invalidates existing sessions. Devise returns nil for a blank hash; this returns "". A hash
// shorter than 29 bytes is returned whole.
func SaltOf(hash string) string {
	if len(hash) < 29 {
		return hash
	}
	return hash[:29]
}

// SaltToken is a non-reversible per-user token derived from the hash's salt, for the session
// cookie: base64url(first 16 bytes of SHA-256("serve-html-markdown salt v1:" + SaltOf(hash))).
// Like SaltOf it changes when the password changes, but the cookie does not leak the raw bcrypt
// salt. It returns "" for a blank hash; callers must reject an empty token.
func SaltToken(hash string) string {
	if hash == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("serve-html-markdown salt v1:" + SaltOf(hash)))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

var (
	dummyCost = Cost // a variable only so tests can avoid a slow cost-12 hash
	dummyOnce sync.Once
	dummyHash []byte
)

// CheckPasswordConstantTime is CheckPassword for sign-in: when the hash is blank or malformed (for
// example no user has the submitted email) it still spends a bcrypt compare against a dummy hash,
// then returns false, so unknown emails take about as long as wrong passwords. Timing parity is
// best-effort only. The dummy hash is computed on first use (about a second at cost 12), not at
// init, so that first unknown-email attempt is slower than the rest.
func CheckPasswordConstantTime(hash, password string) bool {
	if hash != "" {
		if _, err := bcrypt.Cost([]byte(hash)); err == nil {
			return CheckPassword(hash, password)
		}
	}
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("serve-html-markdown dummy password"), dummyCost)
	})
	bcrypt.CompareHashAndPassword(dummyHash, truncate(password))
	return false
}
