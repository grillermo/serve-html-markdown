package auth_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

func TestBearerOK(t *testing.T) {
	cases := []struct {
		name, token, header string
		want                bool
	}{
		{"match", "tok", "Bearer tok", true},
		{"empty configured token never matches", "", "Bearer ", false},
		{"empty token, empty header", "", "", false},
		{"missing header", "tok", "", false},
		{"wrong token", "tok", "Bearer nope", false},
		{"prefix of the token", "tok", "Bearer to", false},
		{"extra suffix", "tok", "Bearer tok ", false},
		{"lowercase scheme", "tok", "bearer tok", false},
		{"bare token", "tok", "tok", false},
		{"basic scheme", "tok", "Basic tok", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := auth.BearerOK(r, c.token); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBearerOKRejectsRepeatedAuthorizationHeaders(t *testing.T) {
	for _, vals := range [][]string{{"Bearer tok", "Bearer tok"}, {"Bearer tok", "Bearer other"}, {"Bearer other", "Bearer tok"}} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header["Authorization"] = vals
		if auth.BearerOK(r, "tok") {
			t.Errorf("%q accepted", vals)
		}
	}
}

func TestUserContext(t *testing.T) {
	ctx := context.Background()
	if auth.UserFrom(ctx) != nil {
		t.Fatal("empty context has a user")
	}
	u := &store.User{ID: 7, Email: "a@b.c"}
	if got := auth.UserFrom(auth.WithUser(ctx, u)); got != u {
		t.Errorf("got %v", got)
	}
}

// Not parallel: it uses the shared test database.
func TestAuthenticate(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw-123456"), bcrypt.MinCost)
	u, err := store.UpsertUser(ctx, pool, "a@example.com", string(hash))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	codec := auth.NewCodec("secret", "session").WithClock(func() time.Time { return now })
	good := auth.Session{UserID: u.ID, Salt: auth.SaltToken(u.EncryptedPassword), ExpiresAt: now.Add(time.Hour)}

	got, err := auth.Authenticate(ctx, pool, codec, codec.EncodeSession(good))
	if err != nil || got == nil || got.ID != u.ID {
		t.Fatalf("valid: %v %v", got, err)
	}
	bad := map[string]string{
		"empty":      "",
		"garbage":    "x.y",
		"wrong salt": codec.EncodeSession(auth.Session{UserID: u.ID, Salt: "other", ExpiresAt: now.Add(time.Hour)}),
		"no salt":    codec.EncodeSession(auth.Session{UserID: u.ID, ExpiresAt: now.Add(time.Hour)}),
		"no user":    codec.EncodeSession(auth.Session{UserID: u.ID + 1, Salt: good.Salt, ExpiresAt: now.Add(time.Hour)}),
		"expired":    codec.EncodeSession(auth.Session{UserID: u.ID, Salt: good.Salt, ExpiresAt: now.Add(-time.Hour)}),
		"other purpose": auth.NewCodec("secret", "oauth-state").WithClock(func() time.Time { return now }).
			EncodeSession(good),
	}
	for name, v := range bad {
		if got, err := auth.Authenticate(ctx, pool, codec, v); got != nil || err != nil {
			t.Errorf("%s: got %v, %v; want nil, nil", name, got, err)
		}
	}
}
