package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// maxCookieBytes is the cookie size limit: browsers drop cookies larger than about 4 KB, so Seal
// refuses to produce a longer value and Open refuses to look at one (before any hashing or parsing).
const maxCookieBytes = 4096

// ErrCookieTooLarge is returned by Seal when the value would exceed the cookie size limit.
var ErrCookieTooLarge = errors.New("auth: sealed value exceeds the 4096 byte cookie limit")

var errNoKey = errors.New("auth: codec has no key (use NewCodec)")

// Session is the payload of the signed session cookie.
//
// ExpiresAt is not part of the JSON payload: it travels in the envelope that Seal/Open enforce,
// with second precision (EncodeSession truncates it, so a session can expire up to one second
// before the time given).
type Session struct {
	UserID    int64     `json:"u"`
	Salt      string    `json:"s"` // SaltToken(encrypted_password); a password change invalidates the session
	ExpiresAt time.Time `json:"-"`
}

// Codec signs and verifies cookie values: base64url(envelope) + "." + base64url(HMAC-SHA256),
// where the envelope is {"e":<expiry, unix seconds>,"d":<payload>}.
//
// Values are signed, NOT encrypted: the client can read the payload. Do not put secrets in it.
//
// Every Codec is bound to a purpose (see NewCodec), so a cookie sealed for one purpose never opens
// under another. The zero Codec has no key and fails closed: Open returns false, Seal errors.
type Codec struct {
	key []byte
	now func() time.Time
}

// NewCodec derives a signing key from SECRET_KEY_BASE and a purpose such as "session" or
// "oauth-state": key = HMAC-SHA256(secret, "serve-html-markdown/v1/"+purpose). It panics on an
// empty secret (an empty HMAC key would make every cookie forgeable) or an empty purpose (it
// would defeat domain separation), so a misconfiguration fails loudly at startup.
func NewCodec(secretKeyBase, purpose string) Codec {
	if secretKeyBase == "" {
		panic("auth: NewCodec requires a non-empty secret key base")
	}
	if purpose == "" {
		panic("auth: NewCodec requires a non-empty purpose")
	}
	mac := hmac.New(sha256.New, []byte(secretKeyBase))
	mac.Write([]byte("serve-html-markdown/v1/" + purpose))
	return Codec{key: mac.Sum(nil), now: time.Now}
}

// WithClock returns a copy of the codec that reads the time from now. It exists for tests, so
// they can check expiry without sleeping; production code uses NewCodec's time.Now.
func (c Codec) WithClock(now func() time.Time) Codec {
	c.now = now
	return c
}

func (c Codec) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c Codec) sign(payload string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// pack signs an already-built envelope without checking its size.
func (c Codec) pack(envelope []byte) string {
	payload := base64.RawURLEncoding.EncodeToString(envelope)
	return payload + "." + c.sign(payload)
}

type envelope struct {
	Expires int64           `json:"e"`
	Data    json.RawMessage `json:"d"`
}

// Seal JSON-encodes v and returns a signed cookie value that Open accepts until expiresAt
// (second precision, truncated). It returns an error if v cannot be marshalled, if the result
// would exceed the cookie size limit, or if the codec has no key.
func (c Codec) Seal(v any, expiresAt time.Time) (string, error) {
	if len(c.key) == 0 {
		return "", errNoKey
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(envelope{Expires: expiresAt.Unix(), Data: data})
	if err != nil {
		return "", err
	}
	value := c.pack(raw)
	if len(value) > maxCookieBytes {
		return "", ErrCookieTooLarge
	}
	return value, nil
}

// Open verifies value (signature under this codec's purpose, size limit, expiry against the
// codec's clock at the moment of the call) and decodes its payload into v, which must be a
// non-nil pointer. Unknown fields in the payload make it fail, so a cookie of a different shape
// is rejected. It reports false for anything wrong and never panics.
func (c Codec) Open(value string, v any) bool {
	_, ok := c.open(value, v)
	return ok
}

// open is Open that also returns the envelope's expiry.
func (c Codec) open(value string, v any) (time.Time, bool) {
	if len(c.key) == 0 || len(value) > maxCookieBytes {
		return time.Time{}, false
	}
	payload, sig, ok := strings.Cut(value, ".")
	// Compare the canonical base64 strings in constant time: any other spelling of the same MAC
	// (padding, whitespace, non-canonical trailing bits) is rejected.
	if !ok || !hmac.Equal([]byte(sig), []byte(c.sign(payload))) {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return time.Time{}, false
	}
	var env envelope
	if !decodeStrict(raw, &env) || len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return time.Time{}, false
	}
	expires := time.Unix(env.Expires, 0).UTC()
	if !c.clock().Before(expires) {
		return time.Time{}, false
	}
	if !decodeStrict(env.Data, v) {
		return time.Time{}, false
	}
	return expires, true
}

// decodeStrict unmarshals exactly one JSON value, rejecting unknown fields and trailing data.
func decodeStrict(raw []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return false
	}
	return dec.Decode(new(json.RawMessage)) == io.EOF
}

// EncodeSession seals s until s.ExpiresAt. It returns "" (which DecodeSession rejects) if sealing
// fails, which only happens for a zero Codec or an absurdly long Salt.
func (c Codec) EncodeSession(s Session) string {
	value, err := c.Seal(s, s.ExpiresAt)
	if err != nil {
		return ""
	}
	return value
}

// DecodeSession returns the session if the value is authentic for this codec's purpose and not
// expired. ExpiresAt is restored from the envelope. A session without a user (UserID 0) is
// rejected.
func (c Codec) DecodeSession(value string) (Session, bool) {
	var s Session
	expires, ok := c.open(value, &s)
	if !ok || s.UserID == 0 {
		return Session{}, false
	}
	s.ExpiresAt = expires
	return s, true
}
