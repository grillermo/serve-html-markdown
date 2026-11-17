package store

import "errors"

var (
	// ErrNotFound is returned by writes that target a row that does not exist
	// (SetExpansionMode, UpsertScrollPosition for an unknown user). Finders do
	// not use it: they return nil, nil (or "") for a missing row.
	ErrNotFound = errors.New("store: not found")
	// ErrInvalidEmail: the email is blank or not shaped like an address.
	ErrInvalidEmail = errors.New("store: invalid email")
	// ErrInvalidExpansionMode: the mode is not one of ExpansionModes.
	ErrInvalidExpansionMode = errors.New("store: invalid expansion mode")
	// ErrInvalidFileName: the file name is blank.
	ErrInvalidFileName = errors.New("store: file name can't be blank")
	// ErrInvalidAnchor: the anchor is blank or has characters outside [\w\-:.].
	ErrInvalidAnchor = errors.New("store: invalid anchor")
	// ErrBlankToken: the refresh token is blank.
	ErrBlankToken = errors.New("store: refresh token can't be blank")
)
