package email

import "errors"

var (
	// ErrEmailTimeout indicates that no verification email was received within
	// the configured polling timeout.
	ErrEmailTimeout = errors.New("email verification timed out")

	// ErrEmailLogin indicates that the IMAP server refused the login. A retry
	// with the same credentials fails the same way.
	ErrEmailLogin = errors.New("IMAP login failed")
)
