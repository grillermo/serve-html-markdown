package auth

import "golang.org/x/crypto/bcrypt"

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
