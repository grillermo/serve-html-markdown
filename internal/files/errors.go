// Package files resolves served file names against the files directory, picks
// unique names for new files and groups a document with its --v<N> versions.
package files

import "errors"

// Kind classifies an Error so the web layer can pick the HTTP status.
type Kind int

const (
	// KindBadRequest maps to HTTP 400.
	KindBadRequest Kind = iota + 1
	// KindMissing maps to HTTP 404 (the file does not exist).
	KindMissing
	// KindUnsupported maps to HTTP 404 (the extension is not served).
	KindUnsupported
)

// Error is a failure the client caused. The response body is
// {"detail": Message}.
type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string { return e.Message }

// Is matches by kind, so errors.Is(err, ErrBadRequest) is true for every
// error made by BadRequest.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind
}

// HTTPStatus is the status code the Rails app answered with: 400 for a bad
// request, 404 for missing and unsupported files.
func (e *Error) HTTPStatus() int {
	if e.Kind == KindBadRequest {
		return 400
	}
	return 404
}

// Kind sentinels, for errors.Is(err, files.ErrBadRequest) and the like. They
// are shared values: never return or modify them as a real error (the errors
// this package returns are fresh *Error values with their own messages), and
// never change their fields.
var (
	ErrBadRequest = &Error{Kind: KindBadRequest, Message: "Bad request."}
	ErrMissing    = &Error{Kind: KindMissing, Message: "File not found."}
	// ErrUnsupported is the sentinel for an extension that is not served.
	ErrUnsupported = &Error{Kind: KindUnsupported, Message: "Only .html, .md, and .markdown files are supported."}
)

// BadRequest makes a 400 error with msg as the detail.
func BadRequest(msg string) error { return &Error{Kind: KindBadRequest, Message: msg} }

// Missing makes a 404 error with msg as the detail.
func Missing(msg string) error { return &Error{Kind: KindMissing, Message: msg} }

// StatusOf returns the HTTP status and detail for err when it is an *Error.
func StatusOf(err error) (status int, detail string, ok bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.HTTPStatus(), e.Message, true
	}
	return 0, "", false
}
