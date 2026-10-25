// Package email reads verification codes from a mailbox.
package email

import (
	"context"
	"time"
)

// Provider is the interface for email verification services.
type Provider interface {
	// Address returns the address of the mailbox with the local part local.
	Address(local string) string

	// WaitForVerificationCode polls for a verification email sent to the given
	// address and returns the verification code extracted from it.
	WaitForVerificationCode(ctx context.Context, address string, since time.Time, timeout time.Duration) (string, error)
}
