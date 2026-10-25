package account

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // G505: RFC 6238 requires HMAC-SHA1.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

const totpWindowSafetyThreshold = 3 // seconds

// totpStep is the length of one TOTP window in seconds (RFC 6238).
const totpStep = 30

// GenerateTOTP generates a 6-digit TOTP code from the given base32-encoded secret.
// Uses the standard TOTP algorithm (RFC 6238) with a 30-second time step.
//
// If the current TOTP window has fewer than 3 seconds remaining, GenerateTOTP
// waits for the next window before generating the code. This prevents the code
// from expiring before the server processes it.
func GenerateTOTP(ctx context.Context, secret string) (string, error) {
	return generateTOTPAfterWindow(ctx, secret, time.Now())
}

// generateTOTPAfterWindow waits out a window that is about to end at now, then
// generates the code. It takes now so that a test can pick the branch.
func generateTOTPAfterWindow(ctx context.Context, secret string, now time.Time) (string, error) {
	remaining := totpRemaining(now)
	if remaining < totpWindowSafetyThreshold {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for next TOTP window: %w", ctx.Err())
		case <-time.After(time.Duration(remaining) * time.Second):
		}
	}

	return generateTOTPAt(secret, time.Now())
}

// CurrentTOTP returns the 6-digit TOTP code of the window that holds now. It
// also returns the whole seconds left in that window, rounded up, from 1 to 30.
// It never waits, so a display shows the code that an authenticator app shows.
func CurrentTOTP(secret string, now time.Time) (code string, secondsLeft int, err error) {
	code, err = generateTOTPAt(secret, now)
	if err != nil {
		return "", 0, err
	}
	return code, totpRemaining(now), nil
}

// totpRemaining returns the whole seconds left at now in its TOTP window, rounded up.
func totpRemaining(now time.Time) int {
	return totpStep - int(now.Unix()%totpStep)
}

// generateTOTPAt returns the 6-digit TOTP code of the window that holds t. It
// takes the time, so each caller picks its own clock and a test picks a fixed time.
func generateTOTPAt(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}

	counter := uint64(t.Unix()) / totpStep //nolint:gosec // G115: Unix time is not negative here.

	// HMAC-SHA1 over the counter.
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	hash := mac.Sum(nil)

	// Dynamic truncation.
	offset := hash[len(hash)-1] & 0x0f
	code := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff

	// 6-digit code.
	return fmt.Sprintf("%06d", code%1000000), nil
}

// decodeSecret decodes a base32-encoded TOTP secret.
func decodeSecret(secret string) ([]byte, error) {
	secret = strings.TrimRight(strings.ToUpper(secret), "=")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return nil, fmt.Errorf("decoding TOTP secret: %w", err)
	}
	return key, nil
}
