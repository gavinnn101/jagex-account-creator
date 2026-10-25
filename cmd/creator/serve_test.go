package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/waittest"
)

// The rows replay the request shapes that a real Chrome sent from a cross-site
// page and from a DNS rebinding page, plus the Host forms of a local browser.
func TestGuardAPI(t *testing.T) {
	const self = "127.0.0.1:8080"
	type headers = map[string]string
	tests := []struct {
		name, method, host string
		hdr                headers
		want               int
	}{
		{"same-origin POST", "POST", self, headers{"Origin": "http://" + self, "Sec-Fetch-Site": "same-origin"}, 204},
		{"POST with neither header (curl)", "POST", self, nil, 204},
		{"typed URL GET (Sec-Fetch-Site none)", "GET", self, headers{"Sec-Fetch-Site": "none"}, 204},
		{"cross-site POST with Origin only (old browser)", "POST", self, headers{"Origin": "http://evil.example"}, 403},
		{"cross-site POST", "POST", self, headers{"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example"}, 403},
		{"same-site POST from localhost:3000", "POST", self, headers{"Sec-Fetch-Site": "same-site", "Origin": "http://localhost:3000"}, 403},
		{"POST with Origin null (sandboxed frame, old browser)", "POST", self, headers{"Origin": "null"}, 403},
		{"cross-site GET of a stream", "GET", self, headers{"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example"}, 403},
		{"same-site GET", "GET", self, headers{"Sec-Fetch-Site": "same-site"}, 403},
		{"rebinding POST, same-origin to the browser", "POST", "evil.example:8080", headers{"Origin": "http://evil.example:8080", "Sec-Fetch-Site": "same-origin"}, 421},
		{"rebinding GET", "GET", "evil.example:8080", headers{"Sec-Fetch-Site": "same-origin"}, 421},
		{"rebinding through 127.0.0.1.nip.io", "GET", "127.0.0.1.nip.io:8080", nil, 421},
		{"rebinding name with no port", "GET", "evil.example", nil, 421},
		{"Host localhost", "GET", "localhost:8080", nil, 204},
		{"Host [::1]", "GET", "[::1]:8080", nil, 204},
		{"Host localhost with no port", "GET", "localhost", nil, 204},
		{"Host 127.0.0.1 with no port", "GET", "127.0.0.1", nil, 204},
		{"Host [::1] with no port", "GET", "[::1]", nil, 204},
		{"empty Host", "GET", "", nil, 421},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			h := guardAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))
			r := httptest.NewRequest(tt.method, "/api/config", http.NoBody)
			r.Host = tt.host
			for k, v := range tt.hdr {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()

			h.ServeHTTP(w, r)

			if w.Code != tt.want || reached != (tt.want == http.StatusNoContent) {
				t.Fatalf("status = %d, reached the API = %v, want %d", w.Code, reached, tt.want)
			}
			if !reached {
				return
			}
			if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
				t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", got)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

// listenLoopback returns a listener on a free loopback port.
func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// newServeAPI returns an apiServer with both hubs that logs to w.
func newServeAPI(w io.Writer) *apiServer {
	return &apiServer{logger: logtest.New(w), events: newHub[api.RunState](runEventBuffer), logs: newHub[logLine](logLineBuffer)}
}

// startServe starts serveAPI with h on ln as the work of a run. Its apiServer
// logs to buf and starts no run.
func startServe(t *testing.T, ln net.Listener, h http.Handler, buf *bytes.Buffer) *runControl {
	t.Helper()
	s := newServeAPI(buf)
	return startRun(t.Context(), func(ctx context.Context, stopNow <-chan struct{}) error {
		return serveAPI(ctx, stopNow, s, ln, h)
	})
}

// waitRun waits for the end of c under the waittest guard and returns its error.
func waitRun(t *testing.T, c *runControl) error {
	t.Helper()
	var wg sync.WaitGroup
	var err error
	wg.Go(func() { err = c.Wait() })
	waittest.Wait(t, &wg)
	return err
}

// assertClosed fails the test when ln is still open.
func assertClosed(t *testing.T, ln net.Listener) {
	t.Helper()
	if err := ln.Close(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("a second Close of the listener = %v, want net.ErrClosed", err)
	}
}

// newTestClient returns an HTTP client with its own connections, under a guard.
func newTestClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func TestServeAPIStop(t *testing.T) {
	ln := listenLoopback(t)
	var buf bytes.Buffer
	c := startServe(t, ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), &buf)

	resp, err := newTestClient(t).Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	// The kept-alive connection is idle, so Shutdown closes it and returns with no error.
	c.Stop()
	if err := waitRun(t, c); err != nil {
		t.Errorf("serveAPI() = %v, want nil", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a stop with no open request logged:\n%s", buf.String())
	}
	assertClosed(t, ln)
}

func TestServeAPIStopNow(t *testing.T) {
	ln := listenLoopback(t)
	var buf bytes.Buffer
	entered := make(chan struct{})
	c := startServe(t, ln, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}), &buf)

	client := newTestClient(t)
	got := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
		got <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the request did not reach the handler")
	}

	// The open request holds Shutdown until the stop-at-once stage, and Close then ends it.
	c.Stop()
	c.StopNow()
	if err := waitRun(t, c); err != nil {
		t.Errorf("serveAPI() = %v, want nil", err)
	}
	select {
	case err := <-got:
		if err == nil {
			t.Error("the open request got an answer, want a closed connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the open request did not end")
	}
	const want = `level=WARN msg="closing the API connections that are still open" error="context canceled"` + "\n"
	if buf.String() != want {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
	assertClosed(t, ln)
}

// The stop of serve ends an open stream, so Shutdown finds no open request.
func TestServeAPIStopEndsStream(t *testing.T) {
	ln := listenLoopback(t)
	var buf bytes.Buffer
	s := newServeAPI(&buf)
	c := startRun(t.Context(), func(ctx context.Context, stopNow <-chan struct{}) error {
		return serveAPI(ctx, stopNow, s, ln, guardAPI(s.handler()))
	})

	// The stream flushes its headers at once, so Get returns before the first log line.
	resp, err := newTestClient(t).Get("http://" + ln.Addr().String() + "/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusOK || ct != "text/event-stream" {
		t.Fatalf("status %d, Content-Type %q, want 200 text/event-stream", resp.StatusCode, ct)
	}

	c.Stop()
	if err := waitRun(t, c); err != nil {
		t.Errorf("serveAPI() = %v, want nil", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a stop with an open stream logged:\n%s", buf.String())
	}
	if body, err := io.ReadAll(resp.Body); err != nil || len(body) != 0 {
		t.Errorf("the stream body = %q, %v, want a clean end with no event", body, err)
	}
	assertClosed(t, ln)
}

func TestServeAPIServeError(t *testing.T) {
	ln := listenLoopback(t)
	_ = ln.Close()
	var buf bytes.Buffer

	err := waitRun(t, startServe(t, ln, http.NotFoundHandler(), &buf))

	if !errors.Is(err, net.ErrClosed) || !strings.HasPrefix(err.Error(), "serving the API: ") {
		t.Errorf("serveAPI() = %v, want a serving the API error that matches net.ErrClosed", err)
	}
}

// signalWriter is a log writer that closes logged at its first write. The
// server writes from its own goroutine, so one mutex guards the buffer.
type signalWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	once   sync.Once
	logged chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A bytes.Buffer write returns no error.
	n, _ := w.buf.Write(p)
	w.once.Do(func() { close(w.logged) })
	return n, nil
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestRunServe(t *testing.T) {
	t.Run("the store is open while serve serves and closed after", func(t *testing.T) {
		w := &signalWriter{logged: make(chan struct{})}
		s := newServeAPI(w)
		s.cfg = storedConfig(t, "")
		s.cfg.Database = filepath.Join(t.TempDir(), "creator.db")
		c := startRun(t.Context(), func(ctx context.Context, stopNow <-chan struct{}) error {
			return runServe(ctx, stopNow, s, "127.0.0.1:0", builtDashboard)
		})
		select {
		case <-w.logged:
		case <-time.After(5 * time.Second):
			t.Fatal("serve did not log its listen")
		}
		if _, _, err := s.store.Runs(t.Context(), 0, 1); err != nil {
			t.Errorf("a read of the store while serve serves = %v, want nil", err)
		}
		if got := doAPI(t.Context(), s.handler(), "GET", "/", ""); got.Code != http.StatusOK || got.Body.String() != dashboardIndex {
			t.Errorf("GET / = %d %q, want 200 and the index.html of the dashboard", got.Code, got.Body.String())
		}

		c.Stop()
		if err := waitRun(t, c); err != nil {
			t.Errorf("runServe() = %v, want nil", err)
		}
		const prefix = `level=INFO msg="serving the API" addr=127.0.0.1:`
		if got := w.String(); !strings.HasPrefix(got, prefix) || strings.Count(got, "\n") != 1 {
			t.Errorf("log = %q, want one line starting %q", got, prefix)
		}
		if _, _, err := s.store.Runs(t.Context(), 0, 1); err == nil || !strings.HasSuffix(err.Error(), "sql: database is closed") {
			t.Errorf("a read of the store after serve = %v, want a closed database", err)
		}
	})

	// The bad port shows the order: a listen before the store would fail with the listen error.
	t.Run("a store that fails to open binds no port", func(t *testing.T) {
		var buf bytes.Buffer
		s := newServeAPI(&buf)
		s.cfg = storedConfig(t, "")
		s.cfg.Database = filepath.Join(t.TempDir(), "missing", "creator.db")

		err := runServe(t.Context(), nil, s, "127.0.0.1:-1", builtDashboard)

		if err == nil || !strings.HasPrefix(err.Error(), "creating database file: ") || s.store != nil {
			t.Errorf("runServe() = %v, store %v, want the store error and no store", err, s.store)
		}
		if buf.Len() != 0 {
			t.Errorf("a failed store open logged:\n%s", buf.String())
		}
	})

	// The bad port and the database that would open show the order: a refusal after the store would leave the file.
	t.Run("a missing dashboard opens no store", func(t *testing.T) {
		var buf bytes.Buffer
		s := newServeAPI(&buf)
		s.cfg = storedConfig(t, "")
		s.cfg.Database = filepath.Join(t.TempDir(), "creator.db")
		errNoDashboard := errors.New("reading the dashboard: open index.html: file does not exist")

		err := runServe(t.Context(), nil, s, "127.0.0.1:-1", func() (fs.FS, error) { return nil, errNoDashboard })

		if !errors.Is(err, errNoDashboard) || err.Error() != errNoDashboard.Error() || s.store != nil || s.spa != nil {
			t.Errorf("runServe() = %v, store %v, spa %v, want the dashboard error as it is, no store and no spa", err, s.store, s.spa)
		}
		if _, err := os.Stat(s.cfg.Database); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the database file: %v, want fs.ErrNotExist", err)
		}
		if buf.Len() != 0 {
			t.Errorf("a refused start logged:\n%s", buf.String())
		}
	})
}
