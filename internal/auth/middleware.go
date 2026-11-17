package auth

import (
	"context"
	"crypto/subtle"
	"net/http"

	"serve-html-markdown/internal/store"
)

// userKey is the context key for the signed-in user. The context API lives
// here and carries *store.User directly: auth imports store, never the other
// way round, and neither imports web.
type userKey struct{}

// WithUser returns a context carrying the signed-in user.
func WithUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom returns the user stored by WithUser, or nil. requireUser is the only thing that stores
// one, so outside a handler wrapped by it (or on an anonymous request) the result is always nil:
// a handler that needs the user must be registered behind the guard, and must not assume non-nil
// anywhere else.
func UserFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey{}).(*store.User)
	return u
}

// Authenticate resolves a session cookie value to its user. It returns nil, nil
// whenever the visitor is simply signed out: a missing, forged, expired or
// wrong-purpose cookie, a user that no longer exists, or a salt that no longer
// matches the user's password hash (a password change signs every session out).
// The error is only for database failures.
//
// Sessions are stateless, so signing out just clears the cookie: a stolen
// cookie stays valid until it expires or the password changes. That is
// accepted for this single-user app.
func Authenticate(ctx context.Context, db store.DB, codec Codec, cookieValue string) (*store.User, error) {
	if cookieValue == "" {
		return nil, nil
	}
	s, ok := codec.DecodeSession(cookieValue)
	if !ok {
		return nil, nil
	}
	u, err := store.FindUserByID(ctx, db, s.UserID)
	if err != nil || u == nil {
		return nil, err
	}
	want := SaltToken(u.EncryptedPassword)
	if want == "" || subtle.ConstantTimeCompare([]byte(s.Salt), []byte(want)) != 1 {
		return nil, nil
	}
	return u, nil
}

// BearerOK reports whether the request carries "Authorization: Bearer <token>".
// It is false when token is empty, so an unset API_TOKEN never opens the API.
// The whole header is compared in constant time. A request with more than one
// Authorization header is never OK.
func BearerOK(r *http.Request, token string) bool {
	// Exactly one header: with two, a proxy and this server could each pick a
	// different one.
	if token == "" || len(r.Header.Values("Authorization")) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) == 1
}
