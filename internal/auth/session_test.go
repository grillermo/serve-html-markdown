package auth

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Generated with Devise itself (bin/rails runner), password "secret123".
const (
	// RAILS_ENV=development: config.stretches = 12, same as production.
	deviseHashCost12 = "$2a$12$RYO0MvqEKFPX4mRMjuz6yO.F68cSaFlOxWVuSPNWRvmYHy5NDq9Tm"
	// RAILS_ENV=test: config.stretches = 1, which bcrypt-ruby clamps to the minimum cost 4.
	deviseHashCostTest = "$2a$04$CT9ftAqY8qphnQ9XNmJjReSYxJvhugNWvQ5lWUCn7yyPOhV6jvENi"
	// Devise in the test env, password = 72 x "a" + "TAIL". bcrypt-ruby truncates to 72 bytes,
	// so this hash verifies for 72 x "a" followed by anything.
	deviseHashLongPassword = "$2a$04$S6GOv300L91NRRXvf0BRsOfRMl7B0Snl9W5PUYRpigbTnKyhZODr6"
)

func TestDeviseHashCost12Verifies(t *testing.T) {
	t.Parallel()
	// Positive check only: the negative path is covered at cost 4 below, and cost 12 is slow.
	if !CheckPassword(deviseHashCost12, "secret123") {
		t.Fatal("devise cost 12 bcrypt hash should verify")
	}
}

func TestDeviseHashVerifies(t *testing.T) {
	if !CheckPassword(deviseHashCostTest, "secret123") {
		t.Fatal("devise bcrypt hash should verify")
	}
	if CheckPassword(deviseHashCostTest, "wrong") {
		t.Fatal("wrong password verified")
	}
	if CheckPassword(deviseHashCostTest, "") {
		t.Fatal("empty password verified")
	}
}

func TestCheckPasswordMalformedHash(t *testing.T) {
	for _, hash := range []string{"", "x", "$2a$12$short", "not a hash at all", deviseHashCost12[:40], strings.Repeat("$", 100)} {
		if CheckPassword(hash, "secret123") {
			t.Fatalf("malformed hash %q verified", hash)
		}
	}
}

// bcrypt-ruby (and so Devise) silently ignores everything after byte 72; x/crypto/bcrypt
// returns ErrPasswordTooLong. CheckPassword truncates so existing users keep signing in.
func TestCheckPasswordTruncatesAt72Bytes(t *testing.T) {
	base := strings.Repeat("a", 72)
	if !CheckPassword(deviseHashLongPassword, base+"TAIL") {
		t.Fatal("ruby-generated hash for >72 byte password should verify")
	}
	if !CheckPassword(deviseHashLongPassword, base+"anything else, as Ruby would accept") {
		t.Fatal("bytes after 72 must be ignored like bcrypt-ruby")
	}
	if !CheckPassword(deviseHashLongPassword, base) {
		t.Fatal("first 72 bytes alone should verify")
	}
	if CheckPassword(deviseHashLongPassword, base[:71]+"b") {
		t.Fatal("difference inside the first 72 bytes must not verify")
	}
}

func TestHashPassword(t *testing.T) {
	t.Parallel()
	// One cost-12 hash only (slow, especially under -race); truncation semantics are covered at
	// cost 4 by the Ruby-generated hash above.
	long := strings.Repeat("b", 100)
	hash, err := HashPassword(long)
	if err != nil {
		t.Fatalf("long password should hash (truncated like Ruby): %v", err)
	}
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != 12 {
		t.Fatalf("cost = %d, %v; want 12", cost, err)
	}
	if !CheckPassword(hash, long[:72]) {
		t.Fatal("hash of a long password should verify against its first 72 bytes")
	}
}

func TestSaltOf(t *testing.T) {
	if got, want := SaltOf(deviseHashCost12), "$2a$12$RYO0MvqEKFPX4mRMjuz6yO"; got != want {
		t.Fatalf("SaltOf = %q, want %q", got, want) // same as Devise's authenticatable_salt: hash[0,29]
	}
	if got := SaltOf(deviseHashLongPassword); got != "$2a$04$S6GOv300L91NRRXvf0BRsO" {
		t.Fatalf("SaltOf = %q", got)
	}
	if got := SaltOf(""); got != "" {
		t.Fatalf("blank hash should give blank salt, got %q", got)
	}
	if got := SaltOf("short"); got != "short" {
		t.Fatalf("short hash returned as is, got %q", got)
	}
}

func TestSaltToken(t *testing.T) {
	a := SaltToken(deviseHashCost12)
	if a == "" || a != SaltToken(deviseHashCost12) {
		t.Fatalf("token should be stable and non-empty, got %q", a)
	}
	if a == SaltToken(deviseHashCostTest) {
		t.Fatal("different salts must give different tokens")
	}
	if a != SaltToken(deviseHashCost12[:29]+"different-hash-part-same-salt") {
		t.Fatal("token must depend only on the salt part")
	}
	if strings.Contains(a, "RYO0Mv") || len(a) != 22 { // 16 bytes in raw base64url
		t.Fatalf("token leaks the salt or has the wrong size: %q", a)
	}
	if SaltToken("") != "" {
		t.Fatal("blank hash must give an empty token")
	}
}

func TestCheckPasswordConstantTimeSemantics(t *testing.T) {
	dummyCost = 4 // keep the dummy hash fast; set before first use (nothing else uses it)
	if !CheckPasswordConstantTime(deviseHashCostTest, "secret123") {
		t.Fatal("valid hash and password should verify")
	}
	if CheckPasswordConstantTime(deviseHashCostTest, "wrong") {
		t.Fatal("wrong password verified")
	}
	for _, hash := range []string{"", "garbage", "$2a$12$short"} {
		if CheckPasswordConstantTime(hash, "secret123") {
			t.Fatalf("blank/malformed hash %q verified", hash)
		}
	}
}

func newCodecAt(purpose string, now *time.Time) Codec {
	return NewCodec("test-secret-key-base", purpose).WithClock(func() time.Time { return *now })
}

func TestSessionRoundTrip(t *testing.T) {
	codec := NewCodec("test-secret-key-base", "session")
	value := codec.EncodeSession(Session{UserID: 7, Salt: "abc", ExpiresAt: time.Now().Add(time.Hour)})
	got, ok := codec.DecodeSession(value)
	if !ok || got.UserID != 7 || got.Salt != "abc" {
		t.Fatalf("round trip failed: %+v %v", got, ok)
	}
	if _, ok := codec.DecodeSession(value + "x"); ok {
		t.Fatal("tampered cookie accepted")
	}
	expired := codec.EncodeSession(Session{UserID: 7, ExpiresAt: time.Now().Add(-time.Second)})
	if _, ok := codec.DecodeSession(expired); ok {
		t.Fatal("expired cookie accepted")
	}
	if _, ok := codec.DecodeSession(codec.EncodeSession(Session{ExpiresAt: time.Now().Add(time.Hour)})); ok {
		t.Fatal("session without a user accepted")
	}
}

func TestSessionPayloadHasNoExpiry(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	codec := newCodecAt("session", &now)
	exp := now.Add(time.Hour)
	value := codec.EncodeSession(Session{UserID: 7, Salt: "abc", ExpiresAt: exp})
	payload, _, _ := strings.Cut(value, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(payload)
	want := `{"e":` + strconv.FormatInt(exp.Unix(), 10) + `,"d":{"u":7,"s":"abc"}}`
	if string(raw) != want {
		t.Fatalf("envelope = %s, want %s", raw, want)
	}
}

func TestSessionExpiryUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	codec := newCodecAt("session", &now)
	expires := now.Add(time.Hour)
	value := codec.EncodeSession(Session{UserID: 7, Salt: "abc", ExpiresAt: expires})

	got, ok := codec.DecodeSession(value)
	if !ok || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("ExpiresAt did not round trip: %+v %v", got, ok)
	}

	now = expires.Add(-time.Second)
	if _, ok := codec.DecodeSession(value); !ok {
		t.Fatal("rejected before expiry")
	}
	now = expires
	if _, ok := codec.DecodeSession(value); ok {
		t.Fatal("accepted at the expiry instant")
	}
	now = expires.Add(time.Second)
	if _, ok := codec.DecodeSession(value); ok {
		t.Fatal("accepted after expiry")
	}
}

func TestSessionExpiresAtHasSecondPrecision(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	codec := newCodecAt("session", &now)
	in := now.Add(time.Hour + 900*time.Millisecond)
	got, ok := codec.DecodeSession(codec.EncodeSession(Session{UserID: 1, ExpiresAt: in}))
	if !ok || !got.ExpiresAt.Equal(in.Truncate(time.Second)) {
		t.Fatalf("want %v truncated to seconds, got %v (%v)", in, got.ExpiresAt, ok)
	}
}

type oauthState struct {
	Nonce string `json:"n"`
}

func TestSealOpenGenericPayload(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	codec := newCodecAt("oauth-state", &now)
	value, err := codec.Seal(oauthState{Nonce: "abc"}, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var got oauthState
	if !codec.Open(value, &got) || got.Nonce != "abc" {
		t.Fatalf("open failed: %+v", got)
	}
	now = now.Add(10 * time.Minute)
	if codec.Open(value, &got) {
		t.Fatal("expired generic payload accepted: Open must enforce expiry for every payload")
	}
}

func TestPurposesAreSeparated(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	session := NewCodec("test-secret-key-base", "session")
	state := NewCodec("test-secret-key-base", "oauth-state")

	// Same payload shape, so only the key can tell them apart.
	if _, ok := state.DecodeSession(session.EncodeSession(Session{UserID: 7, Salt: "x", ExpiresAt: exp})); ok {
		t.Fatal("session cookie opened under purpose oauth-state")
	}
	if _, ok := session.DecodeSession(state.EncodeSession(Session{UserID: 7, Salt: "x", ExpiresAt: exp})); ok {
		t.Fatal("oauth-state cookie opened under purpose session")
	}
	if _, ok := NewCodec("another-secret", "session").DecodeSession(session.EncodeSession(Session{UserID: 7, ExpiresAt: exp})); ok {
		t.Fatal("cookie signed with another secret accepted")
	}

	// Right key, wrong shape, in both directions.
	value, err := state.Seal(oauthState{Nonce: "abc"}, exp)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.DecodeSession(value); ok {
		t.Fatal("oauth state payload decoded as a session")
	}
	var st oauthState
	if session.Open(session.EncodeSession(Session{UserID: 7, Salt: "x", ExpiresAt: exp}), &st) {
		t.Fatal("session payload decoded as oauth state")
	}
}

func TestNewCodecPanicsOnEmptyArguments(t *testing.T) {
	for name, args := range map[string][2]string{"secret": {"", "session"}, "purpose": {"secret", ""}, "both": {"", ""}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("NewCodec with empty %s must panic", name)
				}
			}()
			NewCodec(args[0], args[1])
		}()
	}
}

func TestZeroCodecFailsClosed(t *testing.T) {
	var zero Codec
	if _, err := zero.Seal("x", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("zero codec sealed a value")
	}
	if zero.EncodeSession(Session{UserID: 1, ExpiresAt: time.Now().Add(time.Hour)}) != "" {
		t.Fatal("zero codec encoded a session")
	}
	// A value signed by an HMAC with an empty key must not open on a zero codec.
	forged := Codec{key: []byte{}}.pack([]byte(`{"e":99999999999,"d":{"u":1,"s":"x"}}`))
	if _, ok := zero.DecodeSession(forged); ok {
		t.Fatal("zero codec opened a value signed with the empty key")
	}
	var s Session
	if zero.Open(forged, &s) {
		t.Fatal("zero codec opened a value")
	}
}

func TestCodecWithNilClockDefaultsToNow(t *testing.T) {
	c := NewCodec("k", "p")
	c.now = nil
	value := c.EncodeSession(Session{UserID: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if _, ok := c.DecodeSession(value); !ok {
		t.Fatal("nil clock should default to time.Now")
	}
}

func TestSealErrors(t *testing.T) {
	c := NewCodec("k", "p")
	exp := time.Now().Add(time.Hour)
	if _, err := c.Seal(make(chan int), exp); err == nil {
		t.Fatal("unmarshalable payload should error")
	}
	if _, err := c.Seal(strings.Repeat("a", maxCookieBytes), exp); err != ErrCookieTooLarge {
		t.Fatalf("oversized payload: err = %v, want ErrCookieTooLarge", err)
	}
	// Years outside 0-9999 are fine: the expiry is unix seconds, not a formatted time.
	if value, err := c.Seal("x", time.Date(12000, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil || value == "" {
		t.Fatalf("far future expiry: %q %v", value, err)
	}
}

// packString builds a validly signed value whose string payload has n bytes.
func packString(c Codec, n int, exp time.Time) string {
	return c.pack([]byte(`{"e":` + strconv.FormatInt(exp.Unix(), 10) + `,"d":"` + strings.Repeat("a", n) + `"}`))
}

func TestCookieSizeLimit(t *testing.T) {
	c := NewCodec("k", "p")
	exp := time.Now().Add(time.Hour)
	// Base64 length steps by 1 or 2 per payload byte, so find the first sizes at and over the cap.
	atCap, over := "", ""
	for n := 0; n < 2*maxCookieBytes && over == ""; n++ {
		v := packString(c, n, exp)
		switch {
		case len(v) == maxCookieBytes:
			atCap = v
		case len(v) > maxCookieBytes:
			over = v
		}
	}
	if atCap == "" || over == "" {
		t.Fatal("could not build values at and over the cap")
	}
	var s string
	if !c.Open(atCap, &s) {
		t.Fatalf("value of exactly %d bytes rejected", len(atCap))
	}
	if len(over) <= maxCookieBytes || c.Open(over, &s) {
		t.Fatalf("validly signed value of %d bytes (> cap) accepted", len(over))
	}
	if maxCookieBytes != 4096 {
		t.Fatalf("cookie limit = %d, want 4096", maxCookieBytes)
	}
}

func TestOpenRejectsMalformedValues(t *testing.T) {
	codec := NewCodec("test-secret-key-base", "session")
	valid := codec.EncodeSession(Session{UserID: 7, Salt: "abc", ExpiresAt: time.Now().Add(time.Hour)})
	payload, sig, _ := strings.Cut(valid, ".")
	b64 := base64.RawURLEncoding.EncodeToString
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)

	// Validly signed envelope text.
	signed := func(raw string) string { p := b64([]byte(raw)); return p + "." + codec.sign(p) }

	// Same MAC bytes spelled differently must not pass.
	sigBytes, _ := base64.RawURLEncoding.DecodeString(sig)
	paddedSig := base64.URLEncoding.EncodeToString(sigBytes)
	stdSig := base64.StdEncoding.EncodeToString(sigBytes)

	cases := map[string]string{
		"empty":                   "",
		"no dot":                  payload,
		"only dot":                ".",
		"empty payload":           "." + sig,
		"empty signature":         payload + ".",
		"multiple dots":           payload + "." + sig + "." + sig,
		"dot in payload":          payload + "." + payload + "." + sig,
		"trailing space":          valid + " ",
		"trailing newline":        valid + "\n",
		"newline inside sig":      payload + "." + sig[:10] + "\n" + sig[10:],
		"leading space":           " " + valid,
		"padded signature":        payload + "." + paddedSig,
		"std base64 signature":    payload + "." + stdSig,
		"non-base64":              "!!!!.????",
		"unicode":                 "héllo.wörld",
		"huge payload":            strings.Repeat("A", 10<<20) + "." + sig,
		"huge both":               strings.Repeat("A", 1<<20) + "." + strings.Repeat("B", 1<<20),
		"signed invalid json":     signed("not json"),
		"signed json array":       signed("[1,2,3]"),
		"signed empty":            signed(""),
		"signed null":             signed("null"),
		"signed empty object":     signed("{}"),
		"signed no data":          signed(`{"e":` + exp + `}`),
		"signed null data":        signed(`{"e":` + exp + `,"d":null}`),
		"signed no expiry":        signed(`{"d":{"u":9,"s":"x"}}`),
		"signed zero expiry":      signed(`{"e":0,"d":{"u":9,"s":"x"}}`),
		"signed string expiry":    signed(`{"e":"` + exp + `","d":{"u":9,"s":"x"}}`),
		"signed unknown envelope": signed(`{"e":` + exp + `,"x":1,"d":{"u":9,"s":"x"}}`),
		"signed unknown field":    signed(`{"e":` + exp + `,"d":{"u":9,"s":"x","admin":true}}`),
		"signed wrong types":      signed(`{"e":` + exp + `,"d":{"u":"seven","s":1}}`),
		"signed trailing data":    signed(`{"e":` + exp + `,"d":{"u":9,"s":"x"}} {}`),
		"signed user zero":        signed(`{"e":` + exp + `,"d":{"u":0,"s":"x"}}`),
		"signed payload scalar":   signed(`{"e":` + exp + `,"d":5}`),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if got, ok := codec.DecodeSession(value); ok {
				t.Fatalf("accepted %.60q: %+v", value, got)
			}
		})
	}

	// Sanity: the helper really produces something DecodeSession accepts when the envelope is fine.
	good := signed(`{"e":` + exp + `,"d":{"u":9,"s":"x"}}`)
	if got, ok := codec.DecodeSession(good); !ok || got.UserID != 9 {
		t.Fatalf("helper-signed valid envelope rejected: %+v %v", got, ok)
	}
	// Open must not panic on a non-pointer or nil destination either.
	if codec.Open(good, nil) || codec.Open(good, Session{}) {
		t.Fatal("Open accepted an invalid destination")
	}
}

func TestOpenRejectsFlippedBits(t *testing.T) {
	codec := NewCodec("test-secret-key-base", "session")
	valid := codec.EncodeSession(Session{UserID: 7, Salt: "abc", ExpiresAt: time.Now().Add(time.Hour)})
	for i := 0; i < len(valid); i++ {
		if valid[i] == '.' {
			continue
		}
		b := []byte(valid)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		if _, ok := codec.DecodeSession(string(b)); ok {
			t.Fatalf("accepted cookie with byte %d altered", i)
		}
	}
}

func TestWarmUpIsIdempotent(t *testing.T) {
	dummyCost = 4 // keep the dummy hash fast; see TestCheckPasswordConstantTimeSemantics
	WarmUp()
	first := string(dummyHash)
	if first == "" {
		t.Fatal("WarmUp did not compute the dummy hash")
	}
	WarmUp()
	if string(dummyHash) != first {
		t.Fatal("WarmUp recomputed the dummy hash")
	}
	if CheckPasswordConstantTime("", "x") {
		t.Fatal("blank hash verified")
	}
}
