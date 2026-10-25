package jagexapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/logtest"
)

// fastRetry returns a retryConfig with negligible delays for fast tests.
func fastRetry() retryConfig {
	return retryConfig{maxRetries: 2, baseDelay: 1 * time.Millisecond}
}

func TestDefaultRetryConfig(t *testing.T) {
	want := retryConfig{maxRetries: 2, baseDelay: 500 * time.Millisecond}
	if got := defaultRetryConfig(); got != want {
		t.Errorf("defaultRetryConfig() = %+v, want %+v", got, want)
	}
}

func TestDoWithRetry_SuccessFirstAttempt(t *testing.T) {
	var calls atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		calls.Add(1)
		return mockResp(200, ""), nil
	}

	req := &client.Request{Method: "GET", URL: "https://example.com"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestDoWithRetry_TransportError_RetriesAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		n := calls.Add(1)
		if n <= 2 {
			return nil, errors.New("connection reset by peer")
		}
		return mockResp(200, ""), nil
	}

	req := &client.Request{Method: "GET", URL: "https://example.com"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestDoWithRetry_WarnLogsPathOnly(t *testing.T) {
	first := true
	do := func(context.Context, *client.Request) (*client.Response, error) {
		if first {
			first = false
			return nil, errors.New("connection reset")
		}
		return mockResp(200, ""), nil
	}

	var buf bytes.Buffer
	req := &client.Request{Method: "GET", URL: "https://example.com/oauth2/auth?login_verifier=lv1"}
	resp, err := doWithRetry(t.Context(), logtest.New(&buf), req, do, fastRetry())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Close()

	const want = `level=WARN msg="retrying request" path=/oauth2/auth method=GET attempt=2 delay=1ms error="connection reset"` + "\n"
	if buf.String() != want {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
	if strings.Contains(buf.String(), "login_verifier") {
		t.Errorf("log = %q, want no login_verifier from the URL", buf.String())
	}
}

func TestDoWithRetry_TransportError_ExhaustsRetries(t *testing.T) {
	var calls atomic.Int32
	transportErr := errors.New("connection refused")
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		calls.Add(1)
		return nil, transportErr
	}

	req := &client.Request{Method: "GET", URL: "https://example.com/api"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if resp != nil {
		_ = resp.Close()
		t.Fatal("expected nil response")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "after 3 attempts: connection refused"; !errors.Is(err, transportErr) || err.Error() != want {
		t.Errorf("error = %v, want %q wrapping %v", err, want, transportErr)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (1 initial + 2 retries)", calls.Load())
	}
}

func TestDoWithRetry_POSTSentOnce(t *testing.T) {
	transportErr := errors.New("connection refused")
	tests := []struct {
		name    string
		resp    *client.Response
		err     error
		wantErr string
	}{
		{name: "transport error", err: transportErr, wantErr: "connection refused"},
		{name: "5xx", resp: mockResp(502, ""), wantErr: "server error: status 502, body: 0 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			do := func(context.Context, *client.Request) (*client.Response, error) {
				calls.Add(1)
				return tt.resp, tt.err
			}

			req := &client.Request{Method: "POST", URL: "https://example.com/api"}
			resp, err := doWithRetry(t.Context(), testLogger(), req, do, fastRetry())
			if resp != nil {
				_ = resp.Close()
				t.Error("expected nil response")
			}
			if err == nil || err.Error() != tt.wantErr || errors.Is(err, transportErr) != (tt.err != nil) {
				t.Errorf("error = %v, want the bare %q", err, tt.wantErr)
			}
			if Refused(err) {
				t.Errorf("error = %v, want no refusal", err)
			}
			if calls.Load() != 1 {
				t.Errorf("calls = %d, want 1 (a POST is sent once)", calls.Load())
			}
		})
	}
}

func TestDoWithRetry_5xx_RetriesAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		n := calls.Add(1)
		if n == 1 {
			return mockResp(502, ""), nil
		}
		return mockResp(200, ""), nil
	}

	req := &client.Request{Method: "GET", URL: "https://example.com"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestDoWithRetry_5xx_ExhaustsRetries(t *testing.T) {
	var calls atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		calls.Add(1)
		return mockResp(503, ""), nil
	}

	req := &client.Request{Method: "GET", URL: "https://example.com"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if resp != nil {
		_ = resp.Close()
		t.Fatal("expected nil response after exhausted retries")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "after 3 attempts: server error: status 503, body: 0 bytes"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestDoWithRetry_5xxStatusCodes(t *testing.T) {
	for _, status := range []int{500, 501, 502, 503, 504, 599} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
				calls.Add(1)
				return mockResp(status, ""), nil
			}

			req := &client.Request{Method: "GET", URL: "https://example.com"}
			_, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
			if want := "after 3 attempts: server error: status " + strconv.Itoa(status) + ", body: 0 bytes"; err == nil || err.Error() != want {
				t.Errorf("error = %v, want %q", err, want)
			}
			if calls.Load() != 3 {
				t.Errorf("status %d: calls = %d, want 3", status, calls.Load())
			}
		})
	}
}

func TestDoWithRetry_4xx_NoRetry(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
				calls.Add(1)
				return mockResp(status, ""), nil
			}

			req := &client.Request{Method: "GET", URL: "https://example.com"}
			resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
			if err != nil {
				t.Fatalf("unexpected error for status %d: %v", status, err)
			}
			defer func() { _ = resp.Close() }()
			if resp.StatusCode != status {
				t.Errorf("status = %d, want %d", resp.StatusCode, status)
			}
			if calls.Load() != 1 {
				t.Errorf("status %d: calls = %d, want 1 (no retry)", status, calls.Load())
			}
		})
	}
}

// closeCounter is a response body that counts its Close calls.
type closeCounter struct {
	io.Reader

	closes *atomic.Int32
}

func (c closeCounter) Close() error {
	c.closes.Add(1)
	return nil
}

func TestDoWithRetry_429_RateLimited(t *testing.T) {
	allHeaders := map[string][]string{
		"retry-after":  {"30"},
		"cf-mitigated": {"challenge"},
		"server":       {"cloudflare"},
		"cf-ray":       {"abc123-LHR"},
		"content-type": {"text/html"},
	}
	// Bytes closes a readable body, then rateLimitedError closes it again.
	tests := []struct {
		name       string
		headers    map[string][]string
		body       io.Reader
		wantErr    string
		wantCloses int32
	}{
		{"with Retry-After", map[string][]string{"retry-after": {"30"}}, strings.NewReader("{}"), "rate limited: status 429, retry-after: 30, body: 2 bytes", 2},
		{"without Retry-After", nil, strings.NewReader(""), "rate limited: status 429, body: 0 bytes", 2},
		{
			"every header", allHeaders, strings.NewReader("<html></html>"),
			"rate limited: status 429, retry-after: 30, cf-mitigated: challenge, server: cloudflare, cf-ray: abc123-LHR, content-type: text/html, body: 13 bytes", 2,
		},
		{"unreadable body", nil, iotest.ErrReader(errors.New("connection reset")), "rate limited: status 429, body: unreadable: connection reset", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls, closes atomic.Int32
			do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
				calls.Add(1)
				return &client.Response{
					StatusCode: 429,
					Headers:    tt.headers,
					Body:       closeCounter{Reader: tt.body, closes: &closes},
				}, nil
			}

			req := &client.Request{Method: "GET", URL: "https://example.com/oauth2/auth?login_verifier=lv1"}
			resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
			if resp != nil {
				_ = resp.Close()
				t.Error("expected nil response")
			}
			if !errors.Is(err, ErrRateLimited) || errors.Is(err, ErrRegistrationFailed) {
				t.Errorf("error = %v, want ErrRateLimited and not ErrRegistrationFailed", err)
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "login_verifier") {
				t.Errorf("error = %q, want no login_verifier from the URL", err)
			}
			if calls.Load() != 1 {
				t.Errorf("calls = %d, want 1 (no retry)", calls.Load())
			}
			if closes.Load() != tt.wantCloses {
				t.Errorf("body closes = %d, want %d", closes.Load(), tt.wantCloses)
			}
		})
	}
}

func TestDoWithRetry_5xx_ClosesEveryBody(t *testing.T) {
	// The explicit Close covers an unreadable body, which Bytes leaves open.
	var calls, closes atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		calls.Add(1)
		return &client.Response{
			StatusCode: 502,
			Headers:    map[string][]string{"server": {"nginx"}},
			Body:       closeCounter{Reader: iotest.ErrReader(errors.New("connection reset")), closes: &closes},
		}, nil
	}

	req := &client.Request{Method: "GET", URL: "https://example.com/oauth2/auth?login_verifier=lv1"}
	_, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())

	const want = "after 3 attempts: server error: status 502, server: nginx, body: unreadable: connection reset"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if closes.Load() != calls.Load() {
		t.Errorf("body closes = %d, want %d (one per attempt)", closes.Load(), calls.Load())
	}
}

func TestDoWithRetry_5xxThen429_StopsRetrying(t *testing.T) {
	var calls atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		if calls.Add(1) == 1 {
			return mockResp(503, ""), nil
		}
		return mockResp(429, ""), nil
	}

	req := &client.Request{Method: "GET", URL: "https://example.com/api"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if resp != nil {
		_ = resp.Close()
		t.Error("expected nil response")
	}
	if want := "rate limited: status 429, body: 0 bytes"; !errors.Is(err, ErrRateLimited) || err.Error() != want {
		t.Errorf("error = %v, want %q wrapping ErrRateLimited", err, want)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (the 429 ends the retries)", calls.Load())
	}
}

func TestDoWithRetry_ContextCanceled_NoRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls atomic.Int32
	do := func(ctx context.Context, _ *client.Request) (*client.Response, error) {
		calls.Add(1)
		return nil, ctx.Err()
	}

	req := &client.Request{Method: "GET", URL: "https://example.com"}
	_, err := doWithRetry(ctx, testLogger(), req, do, fastRetry())
	if !errors.Is(err, context.Canceled) || err.Error() != "context canceled" {
		t.Errorf("error = %v, want the bare context.Canceled", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on cancel)", calls.Load())
	}
}

func TestDoWithRetry_ContextCanceledDuringBackoff(t *testing.T) {
	// The cancel lands 4s into the 10s backoff wait, so the wait ends the call.
	synctest.Test(t, func(t *testing.T) {
		cfg := retryConfig{maxRetries: 2, baseDelay: 10 * time.Second}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(4*time.Second, cancel)

		var calls atomic.Int32
		do := func(context.Context, *client.Request) (*client.Response, error) {
			calls.Add(1)
			return nil, errors.New("connection reset")
		}

		start := time.Now()
		_, err := doWithRetry(ctx, testLogger(), &client.Request{Method: "GET", URL: "https://example.com"}, do, cfg)
		if want := "waiting to retry: context canceled"; !errors.Is(err, context.Canceled) || err.Error() != want {
			t.Errorf("error = %v, want %q wrapping context.Canceled", err, want)
		}
		if elapsed := time.Since(start); elapsed != 4*time.Second {
			t.Errorf("elapsed = %v, want 4s", elapsed)
		}
		if calls.Load() != 1 {
			t.Errorf("calls = %d, want 1 (cancel during backoff)", calls.Load())
		}
	})
}

func TestDoWithRetry_Mixed5xxThenSuccess(t *testing.T) {
	var calls atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		n := calls.Add(1)
		switch n {
		case 1:
			return mockResp(500, ""), nil
		case 2:
			return mockResp(503, ""), nil
		default:
			return mockResp(200, ""), nil
		}
	}

	req := &client.Request{Method: "GET", URL: "https://example.com"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestDoWithRetry_MixedTransportAndServerError(t *testing.T) {
	var calls atomic.Int32
	do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
		n := calls.Add(1)
		switch n {
		case 1:
			return nil, errors.New("connection reset")
		case 2:
			return mockResp(502, ""), nil
		default:
			return mockResp(200, ""), nil
		}
	}

	req := &client.Request{Method: "GET", URL: "https://example.com"}
	resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestDoWithRetry_BackoffTiming(t *testing.T) {
	// The fake clock of the bubble makes each backoff gap exact on any machine.
	synctest.Test(t, func(t *testing.T) {
		cfg := retryConfig{maxRetries: 2, baseDelay: 50 * time.Millisecond}

		var timestamps []time.Time
		do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
			timestamps = append(timestamps, time.Now())
			if len(timestamps) < 3 {
				return nil, errors.New("transient")
			}
			return mockResp(200, ""), nil
		}

		req := &client.Request{Method: "GET", URL: "https://example.com"}
		resp, err := doWithRetry(context.Background(), testLogger(), req, do, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer func() { _ = resp.Close() }()

		if len(timestamps) != 3 {
			t.Fatalf("expected 3 attempts, got %d", len(timestamps))
		}
		if gap := timestamps[1].Sub(timestamps[0]); gap != 50*time.Millisecond {
			t.Errorf("first retry gap = %v, want exactly 50ms", gap)
		}
		if gap := timestamps[2].Sub(timestamps[1]); gap != 100*time.Millisecond {
			t.Errorf("second retry gap = %v, want exactly 100ms", gap)
		}
	})
}

func TestDoWithRetry_2xxPassedThrough(t *testing.T) {
	for _, status := range []int{200, 201, 204, 301, 302, 304} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			do := func(_ context.Context, _ *client.Request) (*client.Response, error) {
				return mockResp(status, ""), nil
			}
			req := &client.Request{Method: "GET", URL: "https://example.com"}
			resp, err := doWithRetry(context.Background(), testLogger(), req, do, fastRetry())
			if err != nil {
				t.Fatalf("unexpected error for status %d: %v", status, err)
			}
			defer func() { _ = resp.Close() }()
			if resp.StatusCode != status {
				t.Errorf("status = %d, want %d", resp.StatusCode, status)
			}
		})
	}
}
