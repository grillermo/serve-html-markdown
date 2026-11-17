package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// maxValueLen bounds what Decode will look at (browsers cap a cookie at about 4 KB), so a huge
// value is rejected before any hashing or parsing.
const maxValueLen = 8 << 10

// Session is the payload of the signed session cookie.
//
// ExpiresAt is stored as an RFC3339 UTC timestamp with second precision: Encode truncates it, so
// a session can expire up to one second before the time given.
type Session struct {
	UserID    int64     `json:"u"`
	Salt      string    `json:"s"` // SaltOf(encrypted_password); a password change invalidates the session
	ExpiresAt time.Time `json:"e"`
}

// Codec signs and verifies cookie values: base64url(payload) + "." + base64url(HMAC-SHA256).
// The payload is signed, not encrypted: do not put secrets in it.
type Codec struct {
	key []byte
	now func() time.Time
}

// NewCodec derives the signing key from SECRET_KEY_BASE. It panics on an empty secret: an empty
// HMAC key would make every cookie forgeable, so fail loudly instead.
func NewCodec(secretKeyBase string) Codec {
	if secretKeyBase == "" {
		panic("auth: NewCodec requires a non-empty secret key base")
	}
	mac := hmac.New(sha256.New, []byte(secretKeyBase))
	mac.Write([]byte("serve-html-markdown session"))
	return Codec{key: mac.Sum(nil), now: time.Now}
}

// WithClock returns a copy of the codec that reads the time from now, for tests.
func (c Codec) WithClock(now func() time.Time) Codec {
	c.now = now
	return c
}

func (c Codec) sign(payload string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// seal and open are the payload-agnostic half of the codec.
func (c Codec) seal(raw []byte) string {
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + c.sign(payload)
}

func (c Codec) open(value string) ([]byte, bool) {
	if len(value) > maxValueLen {
		return nil, false
	}
	payload, sig, ok := strings.Cut(value, ".")
	// Compare the canonical base64 strings in constant time: any other spelling of the same MAC
	// (padding, whitespace, non-canonical trailing bits) is rejected.
	if !ok || !hmac.Equal([]byte(sig), []byte(c.sign(payload))) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	return raw, err == nil
}

func (c Codec) Encode(s Session) string {
	s.ExpiresAt = s.ExpiresAt.UTC().Truncate(time.Second)
	raw, _ := json.Marshal(s) // cannot fail: only ints, strings and a time
	return c.seal(raw)
}

// Decode returns the session if the value is authentic and ExpiresAt is still in the future
// according to the codec's clock at the moment of decoding. It never panics.
func (c Codec) Decode(value string) (Session, bool) {
	raw, ok := c.open(value)
	if !ok {
		return Session{}, false
	}
	var s Session
	if json.Unmarshal(raw, &s) != nil || !c.now().Before(s.ExpiresAt) {
		return Session{}, false
	}
	return s, true
}
