package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/proxy/proxytest"
	"jagex_account_creator/internal/waittest"
)

func TestIsAllowedDomain(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		// Exact matches.
		{"jagex.com", true},
		{"cloudflare.com", true},
		{"ipify.org", true},

		// Subdomain matches.
		{"account.jagex.com", true},
		{"challenges.cloudflare.com", true},
		{"api.ipify.org", true},
		{"deep.sub.jagex.com", true},

		// Case insensitivity.
		{"Jagex.COM", true},
		{"CLOUDFLARE.COM", true},
		{"Account.Jagex.Com", true},

		// Non-allowed domains.
		{"google.com", false},
		{"example.com", false},
		{"evil.com", false},
		{"", false},

		// Partial suffix shouldn't match (notjagex.com is not a subdomain of jagex.com).
		{"notjagex.com", false},
		{"fakecloudflare.com", false},
		{"myipify.org", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := isAllowedDomain(tt.host); got != tt.want {
				t.Errorf("isAllowedDomain(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

// fakeUpstreamProxy is a minimal TCP server that speaks enough HTTP to act as
// an upstream proxy for CONNECT and plain HTTP requests in tests.
//
// For CONNECT, it responds 200 and immediately returns (closing the upstream
// side), which cleanly terminates the forward proxy's bidirectional tunnel.
// With echo set, it echoes after the 200 until the peer closes.
// For plain HTTP, it sends a 200 OK response with body "OK".
type fakeUpstreamProxy struct {
	listener    net.Listener
	wg          sync.WaitGroup
	requireAuth string // expected "Basic <b64>" value; empty = no auth required
	echo        bool

	// For inspection after test.
	mu              sync.Mutex
	connectRequests []string // "host:port" values from CONNECT
	httpRequests    []*http.Request
}

func newFakeUpstream(t *testing.T, requireAuth string) *fakeUpstreamProxy {
	t.Helper()
	f := &fakeUpstreamProxy{requireAuth: requireAuth}
	f.start(t)
	return f
}

// start listens and serves f. Set the config fields before the call, because
// the handlers read them.
func (f *fakeUpstreamProxy) start(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.listener = ln
	f.wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.wg.Go(func() {
				f.handleConn(conn)
			})
		}
	})
}

func (f *fakeUpstreamProxy) addr() string { return f.listener.Addr().String() }

func (f *fakeUpstreamProxy) stop(t *testing.T) {
	t.Helper()
	_ = f.listener.Close()
	waittest.Wait(t, &f.wg)
}

func (f *fakeUpstreamProxy) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}

	// Check auth if required.
	if f.requireAuth != "" {
		got := req.Header.Get("Proxy-Authorization")
		if got != f.requireAuth {
			_, _ = fmt.Fprint(conn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
			return
		}
	}

	if req.Method == "CONNECT" {
		f.mu.Lock()
		f.connectRequests = append(f.connectRequests, req.Host)
		f.mu.Unlock()

		// Respond 200. Without echo, the deferred conn.Close() terminates the
		// tunnel cleanly — the forward proxy's io.Copy from upstream will
		// see EOF and exit, then the client side cleans up on conn.Close().
		_, _ = io.WriteString(conn, established)
		if f.echo {
			_, _ = io.Copy(conn, reader)
		}
	} else {
		f.mu.Lock()
		f.httpRequests = append(f.httpRequests, req)
		f.mu.Unlock()

		_, _ = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// httpUpstream builds an http upstream Proxy, with the default empty scheme, for the fake at addr.
func httpUpstream(t *testing.T, addr, username, password string) Proxy {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return Proxy{Host: host, Port: port, Username: username, Password: password}
}

func TestNewForwardProxyLifecycle(t *testing.T) {
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatalf("NewForwardProxy: %v", err)
	}

	// Addr should be a valid host:port on localhost.
	addr := fp.Addr()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("Addr() = %q, not valid host:port: %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", host)
	}
	if port == "0" {
		t.Error("port should not be 0 after binding")
	}

	// Should accept a connection before Stop.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial forward proxy: %v", err)
	}
	_ = conn.Close()

	// Stop should return without error.
	fp.Stop()

	// After Stop, connections should be refused.
	_, err = net.Dial("tcp", addr)
	if err == nil {
		t.Error("expected connection refused after Stop")
	}
}

func TestNewForwardProxyWithAuth(t *testing.T) {
	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, "127.0.0.1:1", "user", "pass"), discardLogger())
	if err != nil {
		t.Fatalf("NewForwardProxy: %v", err)
	}
	defer fp.Stop()

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if fp.proxyAuth != want {
		t.Errorf("proxyAuth = %q, want %q", fp.proxyAuth, want)
	}
}

func TestNewForwardProxyNoAuth(t *testing.T) {
	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, "127.0.0.1:1", "", ""), discardLogger())
	if err != nil {
		t.Fatalf("NewForwardProxy: %v", err)
	}
	defer fp.Stop()

	if fp.proxyAuth != "" {
		t.Errorf("proxyAuth = %q, want empty", fp.proxyAuth)
	}
}

func TestStopClosesPooledConnections(t *testing.T) {
	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, "127.0.0.1:1", "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	c, s := tcpPair(t)
	defer func() { _ = c.Close() }()
	defer func() { _ = s.Close() }()
	fp.pool.Put(s)

	fp.Stop()

	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read from the peer of a pooled connection = %v, want EOF after Stop", err)
	}
}

func TestNewForwardProxyUnknownSchemeLiteralBypassingParser(t *testing.T) {
	fp, err := NewForwardProxy(t.Context(), Proxy{Scheme: "ftp", Host: "127.0.0.1", Port: "21"}, discardLogger())
	if err == nil {
		fp.Stop()
		t.Fatal("NewForwardProxy succeeded for an ftp upstream")
	}
	if want := `unsupported upstream scheme "ftp"`; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestCONNECTTunnel(t *testing.T) {
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	// Verify the upstream received the CONNECT request.
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.connectRequests) != 1 {
		t.Fatalf("upstream got %d CONNECT requests, want 1", len(upstream.connectRequests))
	}
	if upstream.connectRequests[0] != "account.jagex.com:443" {
		t.Errorf("upstream CONNECT host = %q, want %q", upstream.connectRequests[0], "account.jagex.com:443")
	}
}

func TestCONNECTUpstreamAuth(t *testing.T) {
	upstream := newFakeUpstream(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("right:creds")))
	defer upstream.stop(t)

	tests := []struct {
		name, user, pass string
		wantLine         string
	}{
		{"injects the auth", "right", "creds", "HTTP/1.1 200 Connection Established\r\n"},
		{"relays the refusal", "wrong", "creds", "HTTP/1.1 407 Proxy Authentication Required\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), tt.user, tt.pass), discardLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer fp.Stop()

			_, reader := dialProxy(t, fp.Addr(), connectJagex)
			if line := readStatusLine(t, reader); line != tt.wantLine {
				t.Errorf("status line = %q, want %q", line, tt.wantLine)
			}
		})
	}
}

func TestCONNECTBlockedDomain(t *testing.T) {
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n")
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("blocked CONNECT status = %d, want 403", resp.StatusCode)
	}

	// Upstream should not have received anything.
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.connectRequests) != 0 {
		t.Error("upstream should not receive requests for blocked domains")
	}
}

func TestMalformedRequestClosesConnection(t *testing.T) {
	fp, err := NewForwardProxy(t.Context(), Proxy{Host: "203.0.113.1", Port: "8080"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), "not a request\r\n\r\n")
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading to the close: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("response = %q, want none", got)
	}
}

func TestCONNECTUpstreamDialFailure(t *testing.T) {
	fp, log := startForward(t, httpUpstream(t, closedPort(t), "", ""))

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
		t.Errorf("status line = %q, want 502 Bad Gateway for the upstream failure", line)
	}
	assertUpstreamFailed(t, fp, log, "account.jagex.com", 502, "dialing upstream proxy: dial tcp ")
}

func TestHTTPForwarding(t *testing.T) {
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), getIpify)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("HTTP forward status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "OK" {
		t.Errorf("HTTP forward body = %q, want %q", body, "OK")
	}

	// Verify upstream received the request.
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.httpRequests) != 1 {
		t.Fatalf("upstream got %d HTTP requests, want 1", len(upstream.httpRequests))
	}
}

func TestHTTPForwardingWithAuth(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	upstream := newFakeUpstream(t, wantAuth)
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "u", "p"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), getIpify)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("HTTP forward with auth status = %d, want 200", resp.StatusCode)
	}

	// Verify the upstream saw the Proxy-Authorization header.
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.httpRequests) != 1 {
		t.Fatalf("upstream got %d HTTP requests, want 1", len(upstream.httpRequests))
	}
	gotAuth := upstream.httpRequests[0].Header.Get("Proxy-Authorization")
	if gotAuth != wantAuth {
		t.Errorf("upstream Proxy-Authorization = %q, want %q", gotAuth, wantAuth)
	}
}

func TestHTTPBlockedDomain(t *testing.T) {
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), "GET http://google.com/ HTTP/1.1\r\nHost: google.com\r\n\r\n")
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("blocked HTTP status = %d, want 403", resp.StatusCode)
	}

	// Verify Content-Length: 0 is present for HTTP (not CONNECT) blocked responses.
	if cl := resp.Header.Get("Content-Length"); cl != "0" {
		t.Errorf("Content-Length = %q, want %q", cl, "0")
	}

	// Upstream should not have received anything.
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.httpRequests) != 0 {
		t.Error("upstream should not receive requests for blocked domains")
	}
}

func TestHTTPUpstreamDialFailure(t *testing.T) {
	fp, log := startForward(t, httpUpstream(t, closedPort(t), "", ""))

	_, reader := dialProxy(t, fp.Addr(), getIpify)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
		t.Errorf("status line = %q, want 502 Bad Gateway for the upstream failure", line)
	}
	assertUpstreamFailed(t, fp, log, "api.ipify.org", 502, "dialing upstream proxy: dial tcp ")
}

func TestHostPortStripping(t *testing.T) {
	// Verify that domain filtering works when Host includes a port.
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	tests := []struct {
		host       string
		wantStatus int
	}{
		{"account.jagex.com:443", 200},
		{"evil.com:443", 403},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			_, reader := dialProxy(t, fp.Addr(), fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", tt.host, tt.host))
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestMultipleSequentialRequests(t *testing.T) {
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	for i := range 5 {
		conn, reader := dialProxy(t, fp.Addr(), connectJagex)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("connection %d: reading response: %v", i, err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Errorf("connection %d: status = %d, want 200", i, resp.StatusCode)
		}
		_ = conn.Close()
	}

	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.connectRequests) != 5 {
		t.Errorf("upstream got %d CONNECT requests, want 5", len(upstream.connectRequests))
	}
}

func TestCONNECTAuthHeaderFormat(t *testing.T) {
	// Verify the exact CONNECT request sent to upstream includes proper auth header.
	// Use a raw TCP listener for precise inspection of the forwarded request.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, ln.Addr().String(), "testuser", "testpass"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	dialProxy(t, fp.Addr(), "CONNECT challenges.cloudflare.com:443 HTTP/1.1\r\nHost: challenges.cloudflare.com:443\r\n\r\n")

	// Accept the upstream connection and inspect the raw CONNECT request.
	reader := bufio.NewReader(acceptGuarded(t, ln))
	req, err := http.ReadRequest(reader)
	if err != nil {
		t.Fatal(err)
	}

	if req.Method != "CONNECT" {
		t.Errorf("method = %q, want CONNECT", req.Method)
	}
	if req.Host != "challenges.cloudflare.com:443" {
		t.Errorf("host = %q, want challenges.cloudflare.com:443", req.Host)
	}

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("testuser:testpass"))
	gotAuth := req.Header.Get("Proxy-Authorization")
	if gotAuth != wantAuth {
		t.Errorf("Proxy-Authorization = %q, want %q", gotAuth, wantAuth)
	}
}

func TestStopForceClosesActiveConnections(t *testing.T) {
	// Create an upstream that holds the CONNECT tunnel open.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, ln.Addr().String(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Establish a CONNECT tunnel through the forward proxy.
	_, clientReader := dialProxy(t, fp.Addr(), connectJagex)

	// Accept the upstream connection and respond 200 to hold the tunnel open.
	upConn := acceptGuarded(t, ln)
	req, err := http.ReadRequest(bufio.NewReader(upConn))
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "CONNECT" {
		t.Fatalf("expected CONNECT, got %s", req.Method)
	}
	_, _ = fmt.Fprint(upConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	// Read the 200 on the client side to confirm tunnel is up.
	resp, err := http.ReadResponse(clientReader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("tunnel status = %d, want 200", resp.StatusCode)
	}

	// Stop should force-close the tunnel and return promptly,
	// even though neither end has been closed externally.
	stopped := make(chan struct{})
	go func() {
		fp.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		// Success: Stop force-closed the tunnel connections.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return promptly — tunnel connections were not force-closed")
	}
}

func TestStopForceClosesMultipleTunnels(t *testing.T) {
	// Verify that Stop force-closes ALL active tunnels, not just one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, ln.Addr().String(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		_, clientReader := dialProxy(t, fp.Addr(), connectJagex)

		upConn := acceptGuarded(t, ln)
		_, _ = http.ReadRequest(bufio.NewReader(upConn))
		_, _ = fmt.Fprint(upConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

		resp, _ := http.ReadResponse(clientReader, nil)
		if resp != nil {
			_ = resp.Body.Close()
		}
	}

	stopped := make(chan struct{})
	go func() {
		fp.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		// Success: all tunnels force-closed.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return promptly with multiple active tunnels")
	}
}

func TestStopAfterAllConnectionsClosed(t *testing.T) {
	// Verify Stop works cleanly when connections have already closed normally.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, ln.Addr().String(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Establish a CONNECT tunnel, then let it finish normally.
	clientConn, clientReader := dialProxy(t, fp.Addr(), connectJagex)

	upConn := acceptGuarded(t, ln)
	_, _ = http.ReadRequest(bufio.NewReader(upConn))
	_, _ = fmt.Fprint(upConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	resp, err := http.ReadResponse(clientReader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The half-close ends the copy to the upstream, which closes the proxy's upstream
	// conn. That ends the other copy, which closes the client conn: the EOF marks both closed.
	tc, ok := clientConn.(*net.TCPConn)
	if !ok {
		t.Fatalf("conn is a %T, want *net.TCPConn", clientConn)
	}
	if err := tc.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(clientReader); err != nil || len(rest) != 0 {
		t.Fatalf("reading to the close = %q, %v, want EOF", rest, err)
	}

	// Stop should return promptly — double-close on already-closed conns is safe.
	stopped := make(chan struct{})
	go func() {
		fp.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		// Success.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return promptly after connections already closed")
	}
}

func TestCONNECTDialTimeout(t *testing.T) {
	// The non-routable IP hangs the dial, so the dial timeout answers.
	fp, log := startForward(t, httpUpstream(t, "198.51.100.1:1", "", ""))
	fp.dialTimeout = 100 * time.Millisecond

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	line := readStatusLine(t, reader)

	if line != "HTTP/1.1 502 Bad Gateway\r\n" {
		t.Errorf("status line = %q, want 502 Bad Gateway", line)
	}
	assertUpstreamFailed(t, fp, log, "account.jagex.com", 502, "dialing upstream proxy: dial tcp 198.51.100.1:1: ")
}

func TestHTTPDialTimeout(t *testing.T) {
	fp, log := startForward(t, httpUpstream(t, "198.51.100.1:1", "", ""))
	fp.dialTimeout = 100 * time.Millisecond

	_, reader := dialProxy(t, fp.Addr(), getIpify)
	line := readStatusLine(t, reader)

	if line != "HTTP/1.1 502 Bad Gateway\r\n" {
		t.Errorf("status line = %q, want 502 Bad Gateway", line)
	}
	assertUpstreamFailed(t, fp, log, "api.ipify.org", 502, "dialing upstream proxy: dial tcp 198.51.100.1:1: ")
}

func TestForwardProxyOutlivesItsContext(t *testing.T) {
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)
	ctx, cancel := context.WithCancel(t.Context())
	fp, err := NewForwardProxy(ctx, httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	cancel()
	_, reader := dialProxy(t, fp.Addr(), connectJagex)

	if line := readStatusLine(t, reader); line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Errorf("status line = %q, want 200 Connection Established after the end of ctx", line)
	}
}

// The fake dial holds the dial open until its context ends, so only Stop ends it.
func TestStopEndsDialInFlight(t *testing.T) {
	fp, log := startForward(t, httpUpstream(t, closedPort(t), "", ""))
	dialing := make(chan struct{})
	fp.pool.dialFn = func(ctx context.Context, _ string) (net.Conn, error) {
		close(dialing)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	select {
	case <-dialing:
	case <-time.After(2 * time.Second):
		t.Fatal("the forward proxy did not dial the upstream")
	}

	stopped := make(chan struct{})
	go func() {
		fp.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not end the dial in flight")
	}
	if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
		t.Errorf("status line = %q, want 502 Bad Gateway", line)
	}
	assertUpstreamFailed(t, fp, log, "account.jagex.com", 502, "dialing upstream proxy: context canceled")
}

func TestDefaultDialTimeout(t *testing.T) {
	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, "127.0.0.1:1", "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	if fp.dialTimeout != defaultDialTimeout {
		t.Errorf("dialTimeout = %v, want %v", fp.dialTimeout, defaultDialTimeout)
	}
}

// tcpPair creates a connected pair of TCP connections for testing. The kernel
// completes the handshake into the backlog, so the dial needs no goroutine.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })
	return clientConn, acceptGuarded(t, ln)
}

func TestIsConnAlive(t *testing.T) {
	t.Run("alive", func(t *testing.T) {
		c, s := tcpPair(t)
		defer func() { _ = c.Close() }()
		defer func() { _ = s.Close() }()
		if !isConnAlive(s) {
			t.Error("expected alive connection to return true")
		}
	})

	t.Run("peer_closed", func(t *testing.T) {
		c, s := tcpPair(t)
		_ = c.Close()
		defer func() { _ = s.Close() }()

		readToEOF(t, s)
		if isConnAlive(s) {
			t.Error("expected dead connection to return false")
		}
	})

	t.Run("unexpected_data", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c, s := net.Pipe()
			defer func() { _ = c.Close() }()
			defer func() { _ = s.Close() }()
			go func() { _, _ = c.Write([]byte{1}) }()
			if isConnAlive(s) {
				t.Error("expected connection with unread data to return false")
			}
		})
	})
}

// readToEOF blocks until the peer's FIN reaches conn. The 10s read deadline
// fails the test when the FIN never comes.
func readToEOF(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var buf [1]byte
	if _, err := conn.Read(buf[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("read = %v, want io.EOF", err)
	}
}

func TestUpstreamPoolPutGet(t *testing.T) {
	c, s := tcpPair(t)
	defer func() { _ = c.Close() }()
	defer func() { _ = s.Close() }()

	pool := &upstreamPool{addr: "should-not-be-dialed"}
	pool.Put(s)

	got, err := pool.Get(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Should return the same connection we put in.
	if got.LocalAddr().String() != s.LocalAddr().String() {
		t.Error("Get did not return the pooled connection")
	}

	// Pool is now empty; Get should dial and fail (invalid addr).
	_, err = pool.Get(t.Context(), 100*time.Millisecond)
	if err == nil {
		t.Error("expected error when pool is empty and addr is invalid")
	}
}

func TestUpstreamPoolMaxSize(t *testing.T) {
	pool := &upstreamPool{addr: "unused"}
	t.Cleanup(pool.CloseAll)

	// Create maxPoolSize+1 connections.
	clients := make([]net.Conn, maxPoolSize+1)
	for i := range maxPoolSize + 1 {
		c, s := tcpPair(t)
		clients[i] = c
		t.Cleanup(func() { _ = c.Close() })
		pool.Put(s)
	}

	pool.mu.Lock()
	idle := len(pool.idle)
	pool.mu.Unlock()

	// A pool that kept the overflow conn never closes it, so stop before readToEOF waits out its deadline.
	if idle != maxPoolSize {
		t.Fatalf("pool has %d idle connections, want %d (max)", idle, maxPoolSize)
	}
	// Put closed the overflow connection, so its peer sees EOF.
	readToEOF(t, clients[maxPoolSize])
}

func TestUpstreamPoolCloseAll(t *testing.T) {
	pool := &upstreamPool{addr: "unused"}

	for range 3 {
		c, s := tcpPair(t)
		t.Cleanup(func() { _ = c.Close() })
		pool.Put(s)
	}

	pool.CloseAll()

	pool.mu.Lock()
	idle := len(pool.idle)
	pool.mu.Unlock()

	if idle != 0 {
		t.Errorf("pool has %d idle connections after CloseAll, want 0", idle)
	}
}

func TestUpstreamPoolStaleEviction(t *testing.T) {
	// Create a connection and close the peer to make it stale.
	c, s := tcpPair(t)
	_ = c.Close()

	readToEOF(t, s)
	if isConnAlive(s) {
		t.Fatal("isConnAlive = true after the peer closed")
	}

	// Create a live upstream to dial as fallback.
	upstream := newFakeUpstream(t, "")
	defer upstream.stop(t)

	pool := &upstreamPool{addr: upstream.addr()}
	pool.Put(s)

	// Get should detect the stale connection and dial a new one.
	got, err := pool.Get(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Close() }()

	// Pool should be empty (stale was discarded, new was returned to caller).
	pool.mu.Lock()
	idle := len(pool.idle)
	pool.mu.Unlock()
	if idle != 0 {
		t.Errorf("pool has %d idle connections, want 0", idle)
	}
}

// keepAliveFakeUpstream handles multiple HTTP requests per connection (keep-alive)
// and tracks the number of TCP connections accepted.
type keepAliveFakeUpstream struct {
	listener  net.Listener
	wg        sync.WaitGroup
	mu        sync.Mutex
	connCount int
	reqCount  int
}

func newKeepAliveFakeUpstream(t *testing.T) *keepAliveFakeUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &keepAliveFakeUpstream{listener: ln}
	f.wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connCount++
			f.mu.Unlock()
			f.wg.Go(func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for {
					_, err := http.ReadRequest(reader)
					if err != nil {
						return
					}
					f.mu.Lock()
					f.reqCount++
					f.mu.Unlock()
					_, _ = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")
				}
			})
		}
	})
	return f
}

func (f *keepAliveFakeUpstream) addr() string { return f.listener.Addr().String() }
func (f *keepAliveFakeUpstream) stop(t *testing.T) {
	t.Helper()
	_ = f.listener.Close()
	waittest.Wait(t, &f.wg)
}

func TestHTTPConnectionReuse(t *testing.T) {
	upstream := newKeepAliveFakeUpstream(t)
	defer upstream.stop(t)

	fp, err := NewForwardProxy(t.Context(), httpUpstream(t, upstream.addr(), "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	for i := range 3 {
		conn, reader := dialProxy(t, fp.Addr(), getIpify)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("request %d: read response: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Fatalf("request %d: status = %d, want 200", i, resp.StatusCode)
		}
		if string(body) != "OK" {
			t.Fatalf("request %d: body = %q, want %q", i, body, "OK")
		}

		// The handler puts the connection back before it closes the client, so the EOF marks the pool state.
		if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
			t.Fatalf("request %d: reading to the close = %q, %v, want EOF", i, rest, err)
		}
		_ = conn.Close()
		if n := idleConns(fp); n != 1 {
			t.Fatalf("request %d: idle conns = %d, want 1", i, n)
		}
	}

	upstream.mu.Lock()
	connCount := upstream.connCount
	reqCount := upstream.reqCount
	upstream.mu.Unlock()

	if reqCount != 3 {
		t.Errorf("upstream got %d requests, want 3", reqCount)
	}
	if connCount != 1 {
		t.Errorf("upstream got %d connections, want 1 (should reuse)", connCount)
	}
}

func TestUpstreamClosesBeforeResponse(t *testing.T) {
	tests := []struct {
		name, raw, host, wantErr string
	}{
		{"CONNECT", connectJagex, "account.jagex.com", "reading the CONNECT response: unexpected EOF"},
		{"plain HTTP", getIpify, "api.ipify.org", "reading the upstream response: unexpected EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = http.ReadRequest(bufio.NewReader(c))
				_ = c.Close()
			}()

			fp, log := startForward(t, httpUpstream(t, ln.Addr().String(), "", ""))

			_, reader := dialProxy(t, fp.Addr(), tt.raw)
			if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
				t.Errorf("status line = %q, want 502 Bad Gateway", line)
			}
			assertUpstreamFailed(t, fp, log, tt.host, 502, tt.wantErr)
		})
	}
}

// A pooled connection passes the alive check, so a write that fails reaches the write failure paths.
func TestUpstreamWriteFailure(t *testing.T) {
	tests := []struct {
		name, raw, host, wantErr string
	}{
		{"CONNECT", connectJagex, "account.jagex.com", "writing CONNECT to the upstream: write tcp "},
		{"plain HTTP", getIpify, "api.ipify.org", "writing the request to the upstream: write tcp "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The pool holds the connection, so the forward proxy dials nothing.
			fp, log := startForward(t, httpUpstream(t, closedPort(t), "", ""))
			c, s := tcpPair(t)
			defer func() { _ = c.Close() }()
			defer func() { _ = s.Close() }()
			tcp, ok := s.(*net.TCPConn)
			if !ok {
				t.Fatalf("pooled connection is %T, want *net.TCPConn", s)
			}
			if err := tcp.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			fp.pool.Put(s)

			_, reader := dialProxy(t, fp.Addr(), tt.raw)
			if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
				t.Errorf("status line = %q, want 502 Bad Gateway", line)
			}
			assertUpstreamFailed(t, fp, log, tt.host, 502, tt.wantErr)
		})
	}
}

const (
	connectJagex = "CONNECT account.jagex.com:443 HTTP/1.1\r\nHost: account.jagex.com:443\r\n\r\n"
	connectIpify = "CONNECT api.ipify.org:443 HTTP/1.1\r\nHost: api.ipify.org:443\r\n\r\n"
	getIpify     = "GET http://api.ipify.org/ HTTP/1.1\r\nHost: api.ipify.org\r\n\r\n"
	established  = "HTTP/1.1 200 Connection Established\r\n\r\n"
)

// dialProxy connects to the forward proxy at addr and sends raw. The 5s read
// deadline is the guard on every read that waits for the forward proxy.
func dialProxy(t *testing.T, addr, raw string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}

// readStatusLine returns one raw line, so a test can pin the exact status text.
func readStatusLine(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading status line: %v", err)
	}
	return line
}

// socks5Upstream returns the socks5 Proxy that points at the fake f.
func socks5Upstream(t *testing.T, f *proxytest.SOCKS5, username, password string) Proxy {
	t.Helper()
	p := httpUpstream(t, f.Addr(), username, password)
	p.Scheme = "socks5"
	return p
}

// stallingUpstream accepts connections and never answers.
func stallingUpstream(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		waittest.Wait(t, &wg) // after the wait, the accept loop no longer touches held
		for _, c := range held {
			_ = c.Close()
		}
	})
	return ln.Addr().String()
}

func TestSOCKS5CONNECTTunnel(t *testing.T) {
	f := proxytest.NewSOCKS5(t, proxytest.Config{})
	fp, err := NewForwardProxy(t.Context(), socks5Upstream(t, f, "user", "pw"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	conn, reader := dialProxy(t, fp.Addr(), connectJagex)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("status line = %q, want 200 Connection Established", line)
	}
	if line := readStatusLine(t, reader); line != "\r\n" {
		t.Fatalf("header end = %q, want an empty line", line)
	}

	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if string(got) != "ping" {
		t.Errorf("echo = %q, want %q", got, "ping")
	}

	_, recs := f.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	r := recs[0]
	if !bytes.Equal(r.Methods, []byte{0, 2}) {
		t.Errorf("offered methods = %v, want [0 2]", r.Methods)
	}
	if r.User != "user" || r.Pass != "pw" {
		t.Errorf("credentials = %q/%q, want user/pw", r.User, r.Pass)
	}
	if r.Atyp != 3 || r.Host != "account.jagex.com" || r.Port != 443 {
		t.Errorf("target = ATYP %d %s:%d, want ATYP 3 account.jagex.com:443", r.Atyp, r.Host, r.Port)
	}
}

func TestSOCKS5PasswordWithReservedCharacters(t *testing.T) {
	const user, pass = "us:er", "p@ss:w/rd#%?"
	f := proxytest.NewSOCKS5(t, proxytest.Config{})
	fp, err := NewForwardProxy(t.Context(), socks5Upstream(t, f, user, pass), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("status line = %q, want 200 Connection Established", line)
	}
	_, recs := f.Snapshot()
	if len(recs) != 1 || recs[0].User != user || recs[0].Pass != pass {
		t.Errorf("records = %+v, want one with credentials %q/%q", recs, user, pass)
	}
}

func TestSOCKS5NoCredentials(t *testing.T) {
	f := proxytest.NewSOCKS5(t, proxytest.Config{NoAuth: true})
	fp, err := NewForwardProxy(t.Context(), socks5Upstream(t, f, "", ""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("status line = %q, want 200 Connection Established", line)
	}
	_, recs := f.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	r := recs[0]
	if !bytes.Equal(r.Methods, []byte{0}) {
		t.Errorf("offered methods = %v, want [0]", r.Methods)
	}
	if r.User != "" || r.Pass != "" {
		t.Errorf("credentials = %q/%q, want none", r.User, r.Pass)
	}
	if r.Host != "account.jagex.com" || r.Port != 443 {
		t.Errorf("target = %s:%d, want account.jagex.com:443", r.Host, r.Port)
	}
}

func TestSOCKS5AuthRejected(t *testing.T) {
	f := proxytest.NewSOCKS5(t, proxytest.Config{AuthStatus: 1})
	fp, log := startForward(t, socks5Upstream(t, f, "user", "secretpw"))

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
		t.Errorf("status line = %q, want 502 Bad Gateway", line)
	}
	_, recs := f.Snapshot()
	if len(recs) != 1 || recs[0].User != "user" || recs[0].Pass != "secretpw" {
		t.Errorf("records = %+v, want one with credentials user/secretpw", recs)
	}
	assertUpstreamFailed(t, fp, log, "account.jagex.com", 502,
		"dialing through the SOCKS5 upstream: socks connect tcp "+f.Addr()+"->account.jagex.com:443: username/password authentication failed")
	assertNoSecret(t, log, "secretpw")
}

func TestSOCKS5BlockedDomainMakesNoDial(t *testing.T) {
	f := proxytest.NewSOCKS5(t, proxytest.Config{ServeHTTP: true})
	fp, err := NewForwardProxy(t.Context(), socks5Upstream(t, f, "user", "pw"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Stop()

	for _, raw := range []string{
		"CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n",
		"GET http://google.com/ HTTP/1.1\r\nHost: google.com\r\n\r\n",
	} {
		_, reader := dialProxy(t, fp.Addr(), raw)
		if line := readStatusLine(t, reader); line != "HTTP/1.1 403 Forbidden\r\n" {
			t.Errorf("status line for %q = %q, want 403 Forbidden", raw, line)
		}
	}
	if accepted, _ := f.Snapshot(); accepted != 0 {
		t.Errorf("SOCKS5 upstream accepted %d connections, want 0", accepted)
	}
}

func TestSOCKS5PlainHTTP(t *testing.T) {
	tests := []struct {
		name, url, host string
		wantPort        uint16
		wantURI         string
	}{
		{"default port", "http://api.ipify.org/", "api.ipify.org", 80, "/"},
		{"explicit port", "http://api.ipify.org:8080/?format=json", "api.ipify.org:8080", 8080, "/?format=json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := proxytest.NewSOCKS5(t, proxytest.Config{ServeHTTP: true})
			fp, err := NewForwardProxy(t.Context(), socks5Upstream(t, f, "user", "pw"), discardLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer fp.Stop()

			_, reader := dialProxy(t, fp.Addr(), "GET "+tt.url+" HTTP/1.1\r\nHost: "+tt.host+"\r\n\r\n")
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatalf("reading response: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 || string(body) != "OK" {
				t.Errorf("response = %d %q, want 200 %q", resp.StatusCode, body, "OK")
			}

			_, recs := f.Snapshot()
			if len(recs) != 1 {
				t.Fatalf("records = %d, want 1", len(recs))
			}
			r := recs[0]
			if r.Atyp != 3 || r.Host != "api.ipify.org" || r.Port != tt.wantPort {
				t.Errorf("target = ATYP %d %s:%d, want ATYP 3 api.ipify.org:%d", r.Atyp, r.Host, r.Port, tt.wantPort)
			}
			if r.RequestURI != tt.wantURI {
				t.Errorf("request target = %q, want origin form %q", r.RequestURI, tt.wantURI)
			}
		})
	}
}

func TestSOCKS5DialTimeout(t *testing.T) {
	for _, tt := range []struct{ raw, host, target string }{
		{connectJagex, "account.jagex.com", "account.jagex.com:443"},
		{getIpify, "api.ipify.org", "api.ipify.org:80"},
	} {
		up := stallingUpstream(t)
		p := httpUpstream(t, up, "user", "pw")
		p.Scheme = "socks5"
		fp, log := startForward(t, p)
		fp.dialTimeout = 100 * time.Millisecond

		_, reader := dialProxy(t, fp.Addr(), tt.raw)
		if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
			t.Errorf("status line for %q = %q, want 502 Bad Gateway", tt.raw, line)
		}
		assertUpstreamFailed(t, fp, log, tt.host, 502,
			"dialing through the SOCKS5 upstream: socks connect tcp "+up+"->"+tt.target+": ")
	}
}

func TestSOCKS5StopWithOpenTunnel(t *testing.T) {
	f := proxytest.NewSOCKS5(t, proxytest.Config{})
	fp, err := NewForwardProxy(t.Context(), socks5Upstream(t, f, "user", "pw"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("status line = %q, want 200 Connection Established", line)
	}

	stopped := make(chan struct{})
	go func() {
		fp.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return promptly with an open SOCKS5 tunnel")
	}
}

// exchange is one request that scriptedUpstream expects and its reply.
type exchange struct{ want, reply string }

// scriptedUpstream accepts one connection and runs the exchanges in order.
// Then it echoes until EOF. It reads exactly len(want) bytes, so the test
// pins every byte that the forward proxy sends.
func scriptedUpstream(t *testing.T, exchanges ...exchange) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// The test context ends before the cleanups, so a connection that the
		// forward proxy leaked cannot hang the cleanup below.
		defer context.AfterFunc(t.Context(), func() { _ = conn.Close() })()
		for _, ex := range exchanges {
			got := make([]byte, len(ex.want))
			if n, err := io.ReadFull(conn, got); err != nil {
				t.Errorf("upstream read %q: %v, want %q", got[:n], err, ex.want)
				return
			}
			if string(got) != ex.want {
				t.Errorf("upstream got %q, want %q", got, ex.want)
				return
			}
			if _, err := io.WriteString(conn, ex.reply); err != nil {
				t.Errorf("upstream write: %v", err)
				return
			}
		}
		_, _ = io.Copy(conn, conn)
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("the scripted upstream did not end")
		}
	})
	return ln.Addr().String()
}

// startForward starts a ForwardProxy to upstream. It logs at DEBUG, with no
// time and with phase=browser as in the creator, to the returned buffer. The
// cleanup stops it.
func startForward(t *testing.T, upstream Proxy) (*ForwardProxy, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	fp, err := NewForwardProxy(t.Context(), upstream, logtest.New(&log).With("phase", "browser"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fp.Stop)
	return fp, &log
}

// assertUpstreamFailed stops fp, so every handler has logged. Then it checks
// that log holds one upstream failure line for host and status, and that its
// error starts with errPrefix. The OS part of a net error varies, so a test can pin a prefix.
func assertUpstreamFailed(t *testing.T, fp *ForwardProxy, log *bytes.Buffer, host string, status int, errPrefix string) {
	t.Helper()
	fp.Stop()
	want := fmt.Sprintf(`level=DEBUG msg="upstream failed" phase=browser component=forward-proxy host=%s status=%d error=%q`,
		host, status, errPrefix)
	want = strings.TrimSuffix(want, `"`)
	var got []string
	for line := range strings.Lines(log.String()) {
		if strings.Contains(line, `msg="upstream failed"`) {
			got = append(got, line)
		}
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], want) {
		t.Errorf("upstream failure lines = %q, want one that starts with %q", got, want)
	}
}

// assertNoSecret checks that no line of log holds a secret.
func assertNoSecret(t *testing.T, log *bytes.Buffer, secrets ...string) {
	t.Helper()
	for line := range strings.Lines(log.String()) {
		for _, s := range secrets {
			if strings.Contains(line, s) {
				t.Errorf("log line %q holds %q", line, s)
			}
		}
	}
}

// hostLine is the line that Stop logs for one CONNECT host.
func hostLine(host string, connects, sent, received int) string {
	return fmt.Sprintf(`level=DEBUG msg="upstream bytes by host" phase=browser component=forward-proxy host=%s connects=%d sent=%d received=%d`,
		host, connects, sent, received)
}

// assertHostLines checks the per-host lines in log, in order. Call it after Stop.
func assertHostLines(t *testing.T, log *bytes.Buffer, want ...string) {
	t.Helper()
	var got []string
	for line := range strings.Lines(log.String()) {
		if strings.Contains(line, `msg="upstream bytes by host"`) {
			got = append(got, strings.TrimSuffix(line, "\n"))
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("host lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// connectWithAuth is the CONNECT request that the forward proxy sends to an
// HTTP upstream for hostport, with the Proxy-Authorization value auth.
func connectWithAuth(hostport, auth string) string {
	return "CONNECT " + hostport + " HTTP/1.1\r\nHost: " + hostport + "\r\nProxy-Authorization: " + auth + "\r\n\r\n"
}

// assertBytes stops fp, so its totals are final, and checks them.
func assertBytes(t *testing.T, fp *ForwardProxy, wantSent, wantReceived int) {
	t.Helper()
	fp.Stop()
	sent, received := fp.Bytes()
	if sent != int64(wantSent) || received != int64(wantReceived) {
		t.Errorf("Bytes() = %d sent, %d received, want %d sent, %d received", sent, received, wantSent, wantReceived)
	}
}

// acceptGuarded accepts one connection on ln under a 5s deadline, so a test
// fails when nothing dials. The connection gets the same read deadline.
func acceptGuarded(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		t.Fatalf("listener is a %T, want *net.TCPListener", ln)
	}
	deadline := time.Now().Add(5 * time.Second)
	if err := tl.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	conn, err := tl.Accept()
	if err != nil {
		t.Fatalf("accepting: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	return conn
}

// echoPing sends "ping" through an open tunnel and reads the echo.
func echoPing(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	for _, want := range []string{"HTTP/1.1 200 Connection Established\r\n", "\r\n"} {
		if line := readStatusLine(t, reader); line != want {
			t.Fatalf("response line = %q, want %q", line, want)
		}
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q, want %q", got, "ping")
	}
}

// getOK sends a plain HTTP GET for api.ipify.org through fp and checks the
// "OK" reply. It reads to EOF, so the handler has returned when it returns.
func getOK(t *testing.T, fp *ForwardProxy) {
	t.Helper()
	conn, reader := dialProxy(t, fp.Addr(), "GET http://api.ipify.org/ HTTP/1.1\r\nHost: api.ipify.org\r\nUser-Agent: t\r\n\r\n")
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "OK" {
		t.Fatalf("response = %d %q, want 200 %q", resp.StatusCode, body, "OK")
	}
	readToEOF(t, conn)
}

const okReply = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"

// scriptedUpstream pins every byte of the CONNECT, so the "no auth" row fails
// on any Proxy-Authorization line.
func TestForwardProxyBytesHTTPTunnel(t *testing.T) {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pw"))
	for _, tt := range []struct {
		name, user, pass, connect string
	}{
		{"with auth", "user", "pw", connectWithAuth("account.jagex.com:443", auth)},
		{"no auth", "", "", connectJagex},
	} {
		t.Run(tt.name, func(t *testing.T) {
			addr := scriptedUpstream(t, exchange{tt.connect, established})
			fp, log := startForward(t, httpUpstream(t, addr, tt.user, tt.pass))

			conn, reader := dialProxy(t, fp.Addr(), connectJagex)
			echoPing(t, conn, reader)

			sent, received := len(tt.connect)+len("ping"), len(established)+len("ping")
			assertBytes(t, fp, sent, received)
			assertHostLines(t, log, hostLine("account.jagex.com", 1, sent, received))
		})
	}
}

// The second host sorts first, so the line order shows the sort. The two
// tunnels to one host show that the count adds each tunnel.
func TestForwardProxyBytesByHost(t *testing.T) {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pw"))
	tests := []struct {
		name     string
		upstream func(t *testing.T) Proxy
		// exchange returns the bytes of the CONNECT exchange for hostport, each way.
		exchange func(hostport string) (sent, received int)
	}{
		{
			name: "http",
			// The fake checks auth and serves one CONNECT per connection.
			upstream: func(t *testing.T) Proxy {
				t.Helper()
				f := &fakeUpstreamProxy{requireAuth: auth, echo: true}
				f.start(t)
				t.Cleanup(func() { f.stop(t) })
				return httpUpstream(t, f.addr(), "user", "pw")
			},
			exchange: func(hostport string) (int, int) {
				return len(connectWithAuth(hostport, auth)), len(established)
			},
		},
		{
			// The SOCKS5 handshake is not counted, so a tunnel holds its payload only.
			name: "socks5",
			upstream: func(t *testing.T) Proxy {
				t.Helper()
				return socks5Upstream(t, proxytest.NewSOCKS5(t, proxytest.Config{}), "user", "pw")
			},
			exchange: func(string) (int, int) { return 0, 0 },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp, log := startForward(t, tt.upstream(t))
			for _, raw := range []string{connectIpify, connectIpify, connectJagex} {
				conn, reader := dialProxy(t, fp.Addr(), raw)
				echoPing(t, conn, reader)
			}

			ping := len("ping")
			ipifySent, ipifyReceived := tt.exchange("api.ipify.org:443")
			ipifySent, ipifyReceived = 2*(ipifySent+ping), 2*(ipifyReceived+ping)
			jagexSent, jagexReceived := tt.exchange("account.jagex.com:443")
			jagexSent, jagexReceived = jagexSent+ping, jagexReceived+ping

			assertBytes(t, fp, ipifySent+jagexSent, ipifyReceived+jagexReceived)
			want := []string{
				hostLine("account.jagex.com", 1, jagexSent, jagexReceived),
				hostLine("api.ipify.org", 2, ipifySent, ipifyReceived),
			}
			assertHostLines(t, log, want...)

			fp.Stop() // a second Stop logs nothing
			assertHostLines(t, log, want...)
		})
	}
}

func TestForwardProxyBytesHTTPPooledRelay(t *testing.T) {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	get := "GET / HTTP/1.1\r\nHost: api.ipify.org\r\nUser-Agent: t\r\nProxy-Authorization: " + auth + "\r\n\r\n"
	// The upstream accepts one connection, so the second request must reuse it.
	addr := scriptedUpstream(t, exchange{get, okReply}, exchange{get, okReply})
	fp, log := startForward(t, httpUpstream(t, addr, "u", "p"))

	getOK(t, fp)
	getOK(t, fp)

	assertBytes(t, fp, 2*len(get), 2*len(okReply))
	assertHostLines(t, log)
}

// An upstream connection that cannot carry a second request never goes to the pool.
func TestRelayNotReusable(t *testing.T) {
	get := "GET / HTTP/1.1\r\nHost: api.ipify.org\r\nUser-Agent: t\r\n\r\n"
	tests := []struct{ name, reply string }{
		{"connection close", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK"},
		{"bytes after the response", okReply + "X"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := scriptedUpstream(t, exchange{get, tt.reply})
			fp, _ := startForward(t, httpUpstream(t, addr, "", ""))
			getOK(t, fp)
			if n := idleConns(fp); n != 0 {
				t.Errorf("idle conns = %d, want 0", n)
			}
		})
	}
}

// The SOCKS5 totals hold the tunnel bytes only, because the handshake is not counted.
func TestForwardProxyBytesSOCKS5PlainHTTP(t *testing.T) {
	f := proxytest.NewSOCKS5(t, proxytest.Config{ServeHTTP: true})
	fp, log := startForward(t, socks5Upstream(t, f, "user", "pw"))

	getOK(t, fp)

	const get = "GET / HTTP/1.1\r\nHost: api.ipify.org\r\nUser-Agent: t\r\n\r\n"
	assertBytes(t, fp, len(get), len(okReply))
	assertHostLines(t, log)
}

func TestForwardProxyBytesNoUpstreamTraffic(t *testing.T) {
	tests := []struct {
		name, raw, wantLine string
		upstream            func(t *testing.T) string
		wantHosts           []string
		wantErr             string // the error prefix of the upstream failure line, if any
	}{
		{
			name:     "blocked domain",
			raw:      "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n",
			wantLine: "HTTP/1.1 403 Forbidden\r\n",
			upstream: func(t *testing.T) string {
				t.Helper()
				f := newFakeUpstream(t, "")
				t.Cleanup(func() { f.stop(t) })
				return f.addr()
			},
		},
		{
			name:      "dial failure",
			raw:       connectJagex,
			wantLine:  "HTTP/1.1 502 Bad Gateway\r\n",
			upstream:  closedPort,
			wantHosts: []string{hostLine("account.jagex.com", 1, 0, 0)},
			wantErr:   "dialing upstream proxy: dial tcp ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp, log := startForward(t, httpUpstream(t, tt.upstream(t), "", ""))

			_, reader := dialProxy(t, fp.Addr(), tt.raw)
			if line := readStatusLine(t, reader); line != tt.wantLine {
				t.Fatalf("status line = %q, want %q", line, tt.wantLine)
			}

			assertBytes(t, fp, 0, 0)
			assertHostLines(t, log, tt.wantHosts...)
			if tt.wantErr != "" {
				assertUpstreamFailed(t, fp, log, "account.jagex.com", 502, tt.wantErr)
			}
		})
	}
}

func TestForwardProxyBytesUpstreamRejectsAuth(t *testing.T) {
	upstream := newFakeUpstream(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("right:creds")))
	t.Cleanup(func() { upstream.stop(t) })
	fp, log := startForward(t, httpUpstream(t, upstream.addr(), "wrong", "creds"))

	_, reader := dialProxy(t, fp.Addr(), connectJagex)
	if line := readStatusLine(t, reader); line != "HTTP/1.1 407 Proxy Authentication Required\r\n" {
		t.Fatalf("status line = %q, want 407 Proxy Authentication Required", line)
	}

	connect := connectWithAuth("account.jagex.com:443", "Basic "+base64.StdEncoding.EncodeToString([]byte("wrong:creds")))
	const rejected = "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n"
	assertBytes(t, fp, len(connect), len(rejected))
	assertHostLines(t, log, hostLine("account.jagex.com", 1, len(connect), len(rejected)))
	assertUpstreamFailed(t, fp, log, "account.jagex.com", 407, "the upstream refused the CONNECT")
	assertNoSecret(t, log, "creds", "Basic")
}

// idleConns returns the count of idle connections in the pool of fp.
func idleConns(fp *ForwardProxy) int {
	fp.pool.mu.Lock()
	defer fp.pool.mu.Unlock()
	return len(fp.pool.idle)
}

// closedPort returns a loopback address that refuses connections.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// The upstream accepts one connection, so the CONNECT must reuse the connection of the check.
func TestCheckUpstreamHTTPReusesConnection(t *testing.T) {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pw"))
	connect := connectWithAuth("account.jagex.com:443", auth)
	addr := scriptedUpstream(t, exchange{connect, established})
	fp, log := startForward(t, httpUpstream(t, addr, "user", "pw"))

	if err := fp.CheckUpstream(t.Context()); err != nil {
		t.Fatalf("CheckUpstream: %v", err)
	}
	if n := idleConns(fp); n != 1 {
		t.Fatalf("pool holds %d idle connections after the check, want 1", n)
	}
	conn, reader := dialProxy(t, fp.Addr(), connectJagex)
	echoPing(t, conn, reader)

	assertBytes(t, fp, len(connect)+len("ping"), len(established)+len("ping"))
	assertHostLines(t, log, hostLine("account.jagex.com", 1, len(connect)+len("ping"), len(established)+len("ping")))
}

func TestCheckUpstreamHTTPUnreachable(t *testing.T) {
	fp, log := startForward(t, httpUpstream(t, closedPort(t), "", ""))

	err := fp.CheckUpstream(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "dialing upstream proxy: ") {
		t.Fatalf("CheckUpstream = %v, want a dial error", err)
	}
	if n := idleConns(fp); n != 0 {
		t.Errorf("pool holds %d idle connections after a failed check, want 0", n)
	}

	stopped := make(chan struct{})
	go func() {
		fp.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return promptly after a failed check")
	}
	assertBytes(t, fp, 0, 0)
	assertHostLines(t, log)
}

// The check sends no SOCKS5 bytes, so a bare listener shows the one accept and the close.
func TestCheckUpstreamSOCKS5(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	p := httpUpstream(t, ln.Addr().String(), "user", "pw")
	p.Scheme = "socks5"
	fp, log := startForward(t, p)

	if err := fp.CheckUpstream(t.Context()); err != nil {
		t.Fatalf("CheckUpstream: %v", err)
	}
	if err := ln.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accepting the check connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	readToEOF(t, conn)

	assertBytes(t, fp, 0, 0)
	assertHostLines(t, log)
}

func TestCheckUpstreamSOCKS5Unreachable(t *testing.T) {
	p := httpUpstream(t, closedPort(t), "user", "pw")
	p.Scheme = "socks5"
	fp, _ := startForward(t, p)

	err := fp.CheckUpstream(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "dialing SOCKS5 upstream: ") {
		t.Fatalf("CheckUpstream = %v, want a dial error", err)
	}
}

// The upstream reads the first message of the client and never answers, so only Stop ends the exchange.
func TestStopEndsUnansweredUpstream(t *testing.T) {
	tests := []struct {
		name, scheme string
		wantErr      func(up string) string // the start of the error, for the upstream address up
		wantEnd      string                 // the end of the error, after the address of the upstream
		read         func(*bufio.Reader) bool
	}{
		{
			name: "HTTP CONNECT", scheme: "http",
			wantErr: func(string) string { return "reading the CONNECT response: read tcp " },
			// Stop closes the upstream connection of a CONNECT exchange.
			wantEnd: "use of closed network connection",
			read:    func(r *bufio.Reader) bool { _, err := http.ReadRequest(r); return err == nil },
		},
		{
			name: "SOCKS5 handshake", scheme: "socks5",
			wantErr: func(up string) string {
				return "dialing through the SOCKS5 upstream: socks connect tcp " + up + "->account.jagex.com:443: read tcp "
			},
			// Stop sets a past deadline on the connection of a SOCKS5 handshake.
			wantEnd: "i/o timeout",
			// VER NMETHODS and the two methods that a client with credentials offers.
			read: func(r *bufio.Reader) bool { _, err := io.ReadFull(r, make([]byte, 4)); return err == nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			gotFirst := make(chan struct{})
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				// The test context ends before the cleanups, so a Stop that hangs on this connection cannot hang the cleanup.
				context.AfterFunc(t.Context(), func() { _ = c.Close() })
				if tt.read(bufio.NewReader(c)) {
					close(gotFirst)
				}
			}()
			p := httpUpstream(t, ln.Addr().String(), "user", "pw")
			p.Scheme = tt.scheme
			fp, log := startForward(t, p)

			_, reader := dialProxy(t, fp.Addr(), connectJagex)
			select {
			case <-gotFirst:
			case <-time.After(2 * time.Second):
				t.Fatal("the upstream got no first message")
			}
			stopped := make(chan struct{})
			go func() {
				fp.Stop()
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("Stop() did not end the unanswered exchange")
			}
			if line := readStatusLine(t, reader); line != "HTTP/1.1 502 Bad Gateway\r\n" {
				t.Errorf("status line = %q, want 502 Bad Gateway", line)
			}
			up := ln.Addr().String()
			assertUpstreamFailed(t, fp, log, "account.jagex.com", 502, tt.wantErr(up))
			// The error is the last field of its line.
			if end := "->" + up + ": " + tt.wantEnd + "\"\n"; !strings.Contains(log.String(), end) {
				t.Errorf("log = %q, want an upstream failure that ends with %q", log, end)
			}
		})
	}
}
