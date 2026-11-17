package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
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

func TestDeviseHashVerifies(t *testing.T) {
	for name, hash := range map[string]string{"cost12": deviseHashCost12, "test-cost": deviseHashCostTest} {
		t.Run(name, func(t *testing.T) {
			if !CheckPassword(hash, "secret123") {
				t.Fatal("devise bcrypt hash should verify")
			}
			if CheckPassword(hash, "wrong") {
				t.Fatal("wrong password verified")
			}
		})
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
	// One cost-12 hash only: they are slow, especially under -race.
	long := strings.Repeat("b", 100)
	hash, err := HashPassword(long)
	if err != nil {
		t.Fatalf("long password should hash (truncated like Ruby): %v", err)
	}
	if !strings.HasPrefix(hash, "$2a$12$") {
		t.Fatalf("expected cost 12 hash, got %q", hash)
	}
	if !CheckPassword(hash, long) || !CheckPassword(hash, long[:72]) {
		t.Fatal("long password should verify against its 72 byte prefix")
	}
	if CheckPassword(hash, long[:71]) {
		t.Fatal("shorter password verified")
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

func TestSessionRoundTrip(t *testing.T) {
	codec := NewCodec("test-secret-key-base")
	value := codec.Encode(Session{UserID: 7, Salt: "abc", ExpiresAt: time.Now().Add(time.Hour)})
	got, ok := codec.Decode(value)
	if !ok || got.UserID != 7 || got.Salt != "abc" {
		t.Fatalf("round trip failed: %+v %v", got, ok)
	}
	if _, ok := codec.Decode(value + "x"); ok {
		t.Fatal("tampered cookie accepted")
	}
	expired := codec.Encode(Session{UserID: 7, ExpiresAt: time.Now().Add(-time.Second)})
	if _, ok := codec.Decode(expired); ok {
		t.Fatal("expired cookie accepted")
	}
}

func TestSessionExpiryUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	codec := NewCodec("test-secret-key-base").WithClock(func() time.Time { return now })
	expires := now.Add(time.Hour)
	value := codec.Encode(Session{UserID: 7, Salt: "abc", ExpiresAt: expires})

	got, ok := codec.Decode(value)
	if !ok || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("ExpiresAt did not round trip: %+v %v", got, ok)
	}

	now = expires.Add(-time.Second)
	if _, ok := codec.Decode(value); !ok {
		t.Fatal("rejected before expiry")
	}
	now = expires
	if _, ok := codec.Decode(value); ok {
		t.Fatal("accepted at the expiry instant")
	}
	now = expires.Add(time.Second)
	if _, ok := codec.Decode(value); ok {
		t.Fatal("accepted after expiry")
	}
}

func TestSessionExpiresAtHasSecondPrecision(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	codec := NewCodec("k").WithClock(func() time.Time { return now })
	in := now.Add(time.Hour + 900*time.Millisecond)
	got, ok := codec.Decode(codec.Encode(Session{UserID: 1, ExpiresAt: in}))
	if !ok || !got.ExpiresAt.Equal(in.Truncate(time.Second)) {
		t.Fatalf("want %v truncated to seconds, got %v (%v)", in, got.ExpiresAt, ok)
	}
}

func TestCodecsDoNotAcceptEachOthersCookies(t *testing.T) {
	a, b := NewCodec("secret-a"), NewCodec("secret-b")
	if _, ok := b.Decode(a.Encode(Session{UserID: 1, ExpiresAt: time.Now().Add(time.Hour)})); ok {
		t.Fatal("cookie signed with another secret accepted")
	}
}

func TestEmptySecretPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewCodec(\"\") must panic: an empty secret makes forgeable cookies")
		}
	}()
	NewCodec("")
}

func TestDecodeRejectsMalformedValues(t *testing.T) {
	codec := NewCodec("test-secret-key-base")
	valid := codec.Encode(Session{UserID: 7, Salt: "abc", ExpiresAt: time.Now().Add(time.Hour)})
	payload, sig, _ := strings.Cut(valid, ".")
	b64 := base64.RawURLEncoding.EncodeToString

	// Validly signed, but the payload is not a session.
	signed := func(raw string) string { p := b64([]byte(raw)); return p + "." + codec.sign(p) }

	// Same MAC bytes spelled differently must not pass.
	sigBytes, _ := base64.RawURLEncoding.DecodeString(sig)
	paddedSig := base64.URLEncoding.EncodeToString(sigBytes)
	stdSig := base64.StdEncoding.EncodeToString(sigBytes)

	cases := map[string]string{
		"empty":                "",
		"no dot":               payload,
		"only dot":             ".",
		"empty payload":        "." + sig,
		"empty signature":      payload + ".",
		"multiple dots":        payload + "." + sig + "." + sig,
		"dot in payload":       payload + "." + payload + "." + sig,
		"trailing space":       valid + " ",
		"trailing newline":     valid + "\n",
		"newline inside sig":   payload + "." + sig[:10] + "\n" + sig[10:],
		"leading space":        " " + valid,
		"padded signature":     payload + "." + paddedSig,
		"std base64 signature": payload + "." + stdSig,
		"non-base64":           "!!!!.????",
		"unicode":              "héllo.wörld",
		"huge payload":         strings.Repeat("A", 10<<20) + "." + sig,
		"huge both":            strings.Repeat("A", 1<<20) + "." + strings.Repeat("B", 1<<20),
		"signed invalid json":  signed("not json"),
		"signed json array":    signed("[1,2,3]"),
		"signed wrong types":   signed(`{"u":"seven","s":1,"e":"yesterday"}`),
		"signed empty object":  signed("{}"), // ExpiresAt zero => expired
		"signed null":          signed("null"),
		"signed empty":         signed(""),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if got, ok := codec.Decode(value); ok {
				t.Fatalf("accepted %.60q: %+v", value, got)
			}
		})
	}

	// Sanity: the helper really produces something Decode would accept if the JSON were fine.
	good := signed(`{"u":9,"s":"x","e":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`)
	if got, ok := codec.Decode(good); !ok || got.UserID != 9 {
		t.Fatalf("helper-signed valid payload rejected: %+v %v", got, ok)
	}
}

func TestDecodeRejectsFlippedBits(t *testing.T) {
	codec := NewCodec("test-secret-key-base")
	valid := codec.Encode(Session{UserID: 7, Salt: "abc", ExpiresAt: time.Now().Add(time.Hour)})
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
		if _, ok := codec.Decode(string(b)); ok {
			t.Fatalf("accepted cookie with byte %d altered", i)
		}
	}
}
