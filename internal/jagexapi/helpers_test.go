package jagexapi

// A test helper that two test files share lives here.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/useragent"
)

// testLandingURL stands in for the login page that the start chain reaches.
const (
	testLoginChallenge = "550e8400e29b41d4a716446655440000"
	testLandingURL     = "https://account.jagex.com/en-GB/login/registration-start?login_challenge=" + testLoginChallenge
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// debugLogger returns a logger that writes every level to w as JSON lines.
func debugLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// debugRecords decodes the JSON log lines in buf.
func debugRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

func mockRespHeaders(status int, body string, headers map[string][]string) *client.Response {
	return &client.Response{
		StatusCode: status,
		Headers:    headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func mockResp(status int, body string) *client.Response {
	return mockRespHeaders(status, body, nil)
}

// newTestClient returns a real Client with testHints that the test closes at its end.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c := NewClient(testLogger(), "test", testHints(), "")
	t.Cleanup(c.Close)
	return c
}

// newTestClientWithDoFn creates a Client whose transport is doFn. It keeps a
// real httpcloak inner client, because DebugLogCookies reads its cookie jar.
func newTestClientWithDoFn(t *testing.T, doFn func(ctx context.Context, req *client.Request) (*client.Response, error)) *Client {
	t.Helper()
	c := newTestClient(t)
	c.doFn = doFn
	return c
}

// newTestRC returns a registration client on c for testLandingURL, as StartRegistration returns it.
func newTestRC(c *Client) *RegistrationClient {
	rc := newRegistrationClient(c, c.logger)
	rc.landing = &url.URL{Scheme: "https", Host: accountHost, Path: "/en-GB/login/registration-start", RawQuery: "login_challenge=" + testLoginChallenge}
	rc.page = testLandingURL
	rc.loginChallenge = testLoginChallenge
	return rc
}

// screenURL returns the URL of a screen of the login SPA beside testLandingURL, with its login challenge.
func screenURL(screen string) string {
	return "https://account.jagex.com/en-GB/login/" + screen + "?login_challenge=" + testLoginChallenge
}

// testHints returns client hints with a distinct value in each field.
func testHints() useragent.ClientHints {
	return useragent.ClientHints{
		SecChUA:                `"Chromium";v="155"`,
		SecChUAMobile:          "?0",
		SecChUAPlatform:        `"Windows"`,
		SecChUAArch:            `"x86"`,
		SecChUABitness:         `"64"`,
		SecChUAFullVersion:     `"155.0.8059.40"`,
		SecChUAFullVersionList: `"Chromium";v="155.0.8059.40"`,
		SecChUAModel:           `""`,
		SecChUAPlatformVersion: `"19.0.0"`,
	}
}

// newTestRCWithDoFn creates a RegistrationClient for testLandingURL whose
// transport is doFn.
func newTestRCWithDoFn(t *testing.T, doFn func(ctx context.Context, req *client.Request) (*client.Response, error)) *RegistrationClient {
	t.Helper()
	return newTestRC(newTestClientWithDoFn(t, doFn))
}

// newScriptedClient returns a client whose transport gives the answers in
// order, and the requests that the transport received.
func newScriptedClient(t *testing.T, answers []*client.Response) (*Client, *[]*client.Request) {
	t.Helper()
	var got []*client.Request
	c := newTestClientWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		got = append(got, req)
		if len(got) > len(answers) {
			t.Errorf("request %d (%s %s) has no scripted answer", len(got), req.Method, req.URL)
			return nil, errors.New("no scripted answer")
		}
		return answers[len(got)-1], nil
	})
	return c, &got
}

// newScriptedRC returns a registration client for testLandingURL whose
// transport gives the answers in order, and the requests that the transport received.
func newScriptedRC(t *testing.T, answers []*client.Response) (*RegistrationClient, *[]*client.Request) {
	t.Helper()
	c, got := newScriptedClient(t, answers)
	return newTestRC(c), got
}
