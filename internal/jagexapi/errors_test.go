package jagexapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/sardanioss/httpcloak/client"
)

func TestRefused(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"a status error", fmt.Errorf("submitting password: %w: status 400", ErrRegistrationFailed), true},
		{"a 429", fmt.Errorf("activating TOTP: %w: status 429", ErrRateLimited), true},
		{"a bad state", fmt.Errorf("submitting password: %w: registration state %q, reason: %s", ErrRegistrationFailed, "ROLLBACK", `"TEST_REASON"`), true},
		{"a transport error", errors.New("executing request: connection reset"), false},
		{"a 5xx", errors.New("executing request: server error: status 502"), false},
		{"the end of the context", fmt.Errorf("executing request: %w", context.DeadlineExceeded), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Refused(tt.err); got != tt.want {
				t.Errorf("Refused(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestDescribeResponse(t *testing.T) {
	allHeaders := map[string][]string{
		"content-type": {"text/html"},
		"cf-ray":       {"abc123-LHR"},
		"server":       {"cloudflare"},
		"cf-mitigated": {"challenge"},
		"retry-after":  {"30"},
		"location":     {"/next"},
	}
	longBody := strings.Repeat("x", 512) + strings.Repeat("y", 488)
	tests := []struct {
		name        string
		status      int
		headers     map[string][]string
		body        io.Reader // nil leaves the response with no body
		want        string
		wantSnippet string
		wantLogged  bool
	}{
		{"no headers, empty body", 500, nil, strings.NewReader(""), "status 500, body: 0 bytes", "", true},
		{"no body", 403, nil, nil, "status 403, body: 0 bytes", "", true},
		{"retry-after", 429, map[string][]string{"retry-after": {"30"}}, strings.NewReader(""), "status 429, retry-after: 30, body: 0 bytes", "", true},
		{"cf-mitigated", 403, map[string][]string{"cf-mitigated": {"challenge"}}, strings.NewReader(""), "status 403, cf-mitigated: challenge, body: 0 bytes", "", true},
		{"server", 403, map[string][]string{"server": {"cloudflare"}}, strings.NewReader(""), "status 403, server: cloudflare, body: 0 bytes", "", true},
		{"cf-ray", 403, map[string][]string{"cf-ray": {"abc123-LHR"}}, strings.NewReader(""), "status 403, cf-ray: abc123-LHR, body: 0 bytes", "", true},
		{"content-type", 400, map[string][]string{"content-type": {"application/json"}}, strings.NewReader(`{}`), "status 400, content-type: application/json, body: 2 bytes", "{}", true},
		{"empty header value", 403, map[string][]string{"server": {""}}, strings.NewReader(""), "status 403, body: 0 bytes", "", true},
		{
			"every header in a fixed order", 403, allHeaders, strings.NewReader("<html></html>"),
			"status 403, retry-after: 30, cf-mitigated: challenge, server: cloudflare, cf-ray: abc123-LHR, content-type: text/html, body: 13 bytes",
			"<html></html>", true,
		},
		{"long body", 403, nil, strings.NewReader(longBody), "status 403, body: 1000 bytes", strings.Repeat("x", 512), true},
		{"unreadable body", 502, map[string][]string{"server": {"nginx"}}, iotest.ErrReader(errors.New("connection reset")), "status 502, server: nginx, body: unreadable: connection reset", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := debugLogger(&buf)
			resp := &client.Response{StatusCode: tt.status, Headers: tt.headers}
			if tt.body != nil {
				resp.Body = io.NopCloser(tt.body)
			}

			if got := describeResponse(context.Background(), logger, resp); got != tt.want {
				t.Errorf("describeResponse = %q, want %q", got, tt.want)
			}

			records := debugRecords(t, &buf)
			if !tt.wantLogged {
				if len(records) != 0 {
					t.Errorf("log lines = %v, want none", records)
				}
				return
			}
			if len(records) != 1 {
				t.Fatalf("log lines = %v, want 1", records)
			}
			rec := records[0]
			if rec["level"] != "DEBUG" || rec["msg"] != "error response" || rec["status"] != float64(tt.status) || rec["body"] != tt.wantSnippet {
				t.Errorf("log line = %v, want DEBUG %q with status %d and body %q", rec, "error response", tt.status, tt.wantSnippet)
			}
		})
	}
}
