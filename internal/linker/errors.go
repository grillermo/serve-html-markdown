package linker

import "errors"

// Kind classifies a linker Error, mirroring the Ruby exception classes
// SelectionLinker::NotFound and SelectionLinker::UnsafeMatch.
type Kind string

const (
	KindNotFound    Kind = "NotFound"
	KindUnsafeMatch Kind = "UnsafeMatch"
)

// Error is the error type for every expected linking failure (Ruby
// SelectionLinker::Error). Use errors.As(err, &*linker.Error) to detect it,
// or errors.Is against ErrNotFound / ErrUnsafeMatch to test the kind. The
// Message is the user-facing text copied verbatim from the Ruby app.
type Error struct {
	Kind    Kind
	Message string
}

// Error returns the user-facing message. Every error Link returns carries the
// Ruby app's own message for the raise site (UnsafeMatch has three), never the
// sentinels' text.
func (e *Error) Error() string {
	if e.Message == "" {
		return "linker: " + string(e.Kind)
	}
	return e.Message
}

// Is makes errors.Is(err, ErrNotFound) / ErrUnsafeMatch match by Kind.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind
}

// Label renders the error as "<Kind>: <message>", the form recorded by
// script/golden_linker.rb.
func (e *Error) Label() string { return string(e.Kind) + ": " + e.Message }

var (
	// ErrNotFound matches (errors.Is) any NotFound error. Ruby raises NotFound
	// with a single message, so the sentinel carries it.
	ErrNotFound = &Error{Kind: KindNotFound, Message: notFoundMessage}
	// ErrUnsafeMatch matches (errors.Is) any UnsafeMatch error. Ruby raises
	// UnsafeMatch with a different message per reason (cross-block, code block,
	// overlapping link), so the sentinel has none: it exists only to be
	// compared against and is never returned itself.
	ErrUnsafeMatch = &Error{Kind: KindUnsafeMatch}

	// ErrSourceTooLarge is returned for a source of 2 GiB or more: offsets are
	// int32. (The Ruby has no such limit; the app caps documents far below it.)
	ErrSourceTooLarge = errors.New("linker: source too large")

	// ErrInvalidUTF8 is returned for a source or selection that is not valid
	// UTF-8 (including a numeric entity that decodes to a surrogate). The Ruby
	// implementation raises ArgumentError ("invalid byte sequence in UTF-8")
	// there, which is not a SelectionLinker::Error. It is deliberately not a
	// *Error.
	ErrInvalidUTF8 = errors.New("linker: invalid byte sequence in UTF-8")
)

func notFound() error { return &Error{Kind: KindNotFound, Message: notFoundMessage} }

func unsafeMatch(msg string) error { return &Error{Kind: KindUnsafeMatch, Message: msg} }
