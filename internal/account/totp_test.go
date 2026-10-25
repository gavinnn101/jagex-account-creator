package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// RFC 6238 Appendix B test vectors for HMAC-SHA1.
// Secret: "12345678901234567890" (ASCII) = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" (base32).
func TestGenerateTOTPAt_RFC6238Vectors(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	tests := []struct {
		unixTime int64
		want     string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			tm := time.Unix(tt.unixTime, 0)
			got, err := generateTOTPAt(secret, tm)
			if err != nil {
				t.Fatalf("generateTOTPAt(%d): %v", tt.unixTime, err)
			}
			if got != tt.want {
				t.Errorf("generateTOTPAt(%d) = %q, want %q", tt.unixTime, got, tt.want)
			}
		})
	}
}

func TestGenerateTOTPAt_InvalidSecret(t *testing.T) {
	_, err := generateTOTPAt("!!!invalid!!!", time.Now())
	if err == nil {
		t.Fatal("expected error for invalid base32 secret")
	}
}

func TestGenerateTOTPAt_SecretNormalization(t *testing.T) {
	// Same secret in different forms should produce the same code.
	tm := time.Unix(1234567890, 0)

	forms := []string{
		"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",     // uppercase, no padding
		"gezdgnbvgy3tqojqgezdgnbvgy3tqojq",     // lowercase
		"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ====", // with padding
	}

	var expected string
	for i, secret := range forms {
		got, err := generateTOTPAt(secret, tm)
		if err != nil {
			t.Fatalf("form %d: %v", i, err)
		}
		if i == 0 {
			expected = got
		} else if got != expected {
			t.Errorf("form %d: got %q, want %q", i, got, expected)
		}
	}
}

func TestGenerateTOTPAt_SameWindow(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	// Two times in the same 30-second window should produce the same code.
	t1 := time.Unix(1234567890, 0)    // second 0 of window
	t2 := time.Unix(1234567890+15, 0) // second 15 of same window

	code1, _ := generateTOTPAt(secret, t1)
	code2, _ := generateTOTPAt(secret, t2)
	if code1 != code2 {
		t.Errorf("same window produced different codes: %q vs %q", code1, code2)
	}

	// Next window should produce a different code.
	t3 := time.Unix(1234567890+30, 0)
	code3, _ := generateTOTPAt(secret, t3)
	if code1 == code3 {
		t.Errorf("different windows produced same code: %q", code1)
	}
}

func TestGenerateTOTPAt_OutputFormat(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	code, err := generateTOTPAt(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Errorf("code length = %d, want 6", len(code))
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			t.Errorf("code contains non-digit %q: %q", string(ch), code)
		}
	}
}

func TestCurrentTOTP(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	tests := []struct {
		name     string
		now      time.Time
		wantCode string
		wantLeft int
	}{
		{"window start", time.Unix(1234567890, 0), "005924", 30},
		{"a fraction into the window rounds up", time.Unix(1234567890, 500000000), "005924", 30},
		{"one second into the window", time.Unix(1111111111, 0), "050471", 29},
		{"mid window", time.Unix(2000000000, 0), "279037", 10},
		{"last second of the window", time.Unix(1111111109, 0), "081804", 1},
		{"last second, late in it", time.Unix(59, 999999999), "287082", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, left, err := CurrentTOTP(secret, tt.now)
			if err != nil || code != tt.wantCode || left != tt.wantLeft {
				t.Errorf("CurrentTOTP = %q, %d, %v, want %q, %d, nil", code, left, err, tt.wantCode, tt.wantLeft)
			}
		})
	}
	t.Run("a secret that does not decode", func(t *testing.T) {
		code, left, err := CurrentTOTP("!!!invalid!!!", time.Unix(59, 0))
		if err == nil || !strings.HasPrefix(err.Error(), "decoding TOTP secret: ") || code != "" || left != 0 {
			t.Errorf("CurrentTOTP = %q, %d, %v, want the decoding TOTP secret error", code, left, err)
		}
	})
}

func TestGenerateTOTPAfterWindow(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	windowStart := time.Unix(1234567890, 0)
	// One second before the window end, so the wait is one second.
	windowEnd := windowStart.Add(29 * time.Second)
	// The last instant that still leaves the safety threshold.
	atThreshold := windowStart.Add((30 - totpWindowSafetyThreshold) * time.Second)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name     string
		ctx      context.Context //nolint:containedctx // Table input.
		now      time.Time
		wantErr  error
		wantWait time.Duration
	}{
		{"mid window skips the wait", cancelled, windowStart, nil, 0},
		{"safety threshold skips the wait", cancelled, atThreshold, nil, 0},
		{"window end returns the context error", cancelled, windowEnd, context.Canceled, 0},
		{"window end waits for the next window", context.Background(), windowEnd, nil, time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The fake clock of the bubble makes the wait exact on any machine.
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				code, err := generateTOTPAfterWindow(tt.ctx, secret, tt.now)
				waited := time.Since(start)

				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if tt.wantErr == nil && len(code) != 6 {
					t.Errorf("code = %q, want 6 digits", code)
				}
				if waited != tt.wantWait {
					t.Errorf("waited %v, want exactly %v", waited, tt.wantWait)
				}
			})
		})
	}
}

func TestGenerateTOTP_ProducesValidCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
		// The bubble clock starts on a window start, so 2s remain: below the threshold.
		time.Sleep(28 * time.Second)
		start := time.Now()
		code, err := GenerateTOTP(context.Background(), secret)
		if err != nil {
			t.Fatalf("GenerateTOTP: %v", err)
		}
		if waited := time.Since(start); waited != 2*time.Second {
			t.Errorf("waited %v, want exactly 2s", waited)
		}
		expected, _ := generateTOTPAt(secret, time.Now())
		if code != expected {
			t.Errorf("GenerateTOTP = %q, generateTOTPAt(now) = %q", code, expected)
		}
	})
}

func TestGenerateTOTP_InvalidSecret(t *testing.T) {
	// GenerateTOTP waits at a window edge before it decodes. The bubble clock starts on a window start.
	synctest.Test(t, func(t *testing.T) {
		_, err := GenerateTOTP(context.Background(), "!!!invalid!!!")
		if err == nil || !strings.HasPrefix(err.Error(), "decoding TOTP secret: ") {
			t.Fatalf("err = %v, want a decoding TOTP secret error", err)
		}
	})
}
