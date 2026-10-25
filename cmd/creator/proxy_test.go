package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/proxy"
	"jagex_account_creator/internal/store"
)

func TestStartForwardProxy(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	t.Run("direct connection", func(t *testing.T) {
		fwd, addr, err := startForwardProxy(t.Context(), logger, proxy.Proxy{})
		if err != nil {
			t.Fatalf("startForwardProxy: %v", err)
		}
		if fwd != nil {
			t.Error("forward proxy started for a direct connection")
		}
		if addr != "" {
			t.Errorf("addr = %q, want empty", addr)
		}
	})

	t.Run("proxy without credentials", func(t *testing.T) {
		fwd, addr, err := startForwardProxy(t.Context(), logger, proxy.Proxy{Host: "203.0.113.1", Port: "8080"})
		if err != nil {
			t.Fatalf("startForwardProxy: %v", err)
		}
		if fwd == nil {
			t.Fatal("no forward proxy for a proxy without credentials")
		}
		defer fwd.Stop()
		if addr != fwd.Addr() || !strings.HasPrefix(addr, "127.0.0.1:") {
			t.Errorf("addr = %q, want the local forward proxy %q", addr, fwd.Addr())
		}

		// The allowlist refuses the domain before any upstream dial.
		conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(conn, "CONNECT google.com:443 HTTP/1.1\r\nHost: google.com:443\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatalf("reading status line: %v", err)
		}
		if line != "HTTP/1.1 403 Forbidden\r\n" {
			t.Errorf("status line = %q, want 403 Forbidden", line)
		}
	})

	t.Run("socks5 proxy without credentials", func(t *testing.T) {
		p := proxy.Proxy{Scheme: "socks5", Host: "203.0.113.1", Port: "1080"}
		fwd, addr, err := startForwardProxy(t.Context(), logger, p)
		if err != nil {
			t.Fatalf("startForwardProxy: %v", err)
		}
		if fwd == nil {
			t.Fatal("no forward proxy for a socks5 proxy without credentials")
		}
		defer fwd.Stop()
		if addr != fwd.Addr() || !strings.HasPrefix(addr, "127.0.0.1:") {
			t.Errorf("addr = %q, want the local forward proxy %q", addr, fwd.Addr())
		}
	})

	t.Run("proxy with credentials", func(t *testing.T) {
		p := proxy.Proxy{Host: "203.0.113.1", Port: "8080", Username: "user", Password: "pass"}
		fwd, addr, err := startForwardProxy(t.Context(), logger, p)
		if err != nil {
			t.Fatalf("startForwardProxy: %v", err)
		}
		if fwd == nil {
			t.Fatal("no forward proxy for a proxy with credentials")
		}
		defer fwd.Stop()
		if addr != fwd.Addr() || !strings.HasPrefix(addr, "127.0.0.1:") {
			t.Errorf("addr = %q, want the local forward proxy %q", addr, fwd.Addr())
		}
	})

	t.Run("socks5 proxy with credentials", func(t *testing.T) {
		p := proxy.Proxy{Scheme: "socks5", Host: "203.0.113.1", Port: "1080", Username: "user", Password: "pass"}
		fwd, addr, err := startForwardProxy(t.Context(), logger, p)
		if err != nil {
			t.Fatalf("startForwardProxy: %v", err)
		}
		if fwd == nil {
			t.Fatal("no forward proxy for a socks5 proxy with credentials")
		}
		defer fwd.Stop()
		if addr != fwd.Addr() || !strings.HasPrefix(addr, "127.0.0.1:") {
			t.Errorf("addr = %q, want the local forward proxy %q", addr, fwd.Addr())
		}
	})

	t.Run("unknown scheme literal bypassing the parser", func(t *testing.T) {
		p := proxy.Proxy{Scheme: "ftp", Host: "203.0.113.1", Port: "21", Username: "user", Password: "pass"}
		fwd, addr, err := startForwardProxy(t.Context(), logger, p)
		if err == nil {
			fwd.Stop()
			t.Fatal("startForwardProxy succeeded for an unknown scheme")
		}
		if fwd != nil || addr != "" {
			t.Errorf("fwd = %v, addr = %q, want nil and empty", fwd, addr)
		}
	})
}

// proxyStore opens the store of a new database in a temp dir whose proxies.txt
// holds proxies. Cleanup closes it.
func proxyStore(t *testing.T, proxies string) *store.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proxies.txt"), []byte(proxies), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(t.Context(), logtest.New(io.Discard), &config.Config{Database: filepath.Join(dir, "creator.db")})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRunProxies(t *testing.T) {
	tests := []struct {
		name      string
		direct    bool
		proxies   string
		disable   int64    // the id of a proxy to disable, or 0
		wantHosts []string // the HostPort of each pick, sorted. nil means a direct connection
		wantLog   string
		wantErr   string
	}{
		{name: "direct ignores the list", direct: true, proxies: "a:1\n", wantLog: `level=WARN msg="proxy.direct is set, using direct connection"`},
		{name: "direct with no list", direct: true, wantLog: `level=WARN msg="proxy.direct is set, using direct connection"`},
		{name: "the enabled proxies", proxies: "a:1\nb:2:u:p\nc:3\n", disable: 2, wantHosts: []string{"a:1", "c:3"}, wantLog: `level=INFO msg="proxies loaded" count=2`},
		{name: "one enabled proxy", proxies: "a:1\n", wantHosts: []string{"a:1"}, wantLog: `level=INFO msg="proxies loaded" count=1`},
		{name: "no proxy", wantErr: "no enabled proxy: import or enable a proxy, or set proxy.direct"},
		{name: "no enabled proxy", proxies: "a:1\n", disable: 1, wantErr: "no enabled proxy: import or enable a proxy, or set proxy.direct"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := proxyStore(t, tt.proxies)
			if tt.disable != 0 {
				if err := s.SetProxyEnabled(t.Context(), tt.disable, false); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			cfg := &config.Config{Proxy: config.ProxyConfig{Direct: tt.direct}}
			m, err := runProxies(t.Context(), logtest.New(&buf), cfg, s)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr || m != nil {
					t.Fatalf("runProxies = %v, %v; want the error %q", m, err, tt.wantErr)
				}
				if buf.Len() != 0 {
					t.Errorf("log = %q, want none", buf.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("runProxies: %v", err)
			}
			if got := pickedHosts(t, m, len(tt.wantHosts)); !slices.Equal(got, tt.wantHosts) {
				t.Errorf("picks = %v, want %v", got, tt.wantHosts)
			}
			if got := strings.TrimSuffix(buf.String(), "\n"); got != tt.wantLog {
				t.Errorf("log = %q, want %q", got, tt.wantLog)
			}
		})
	}
}

// pickedHosts holds n picks of m and returns the HostPort of each, sorted. With
// n 0, it checks that m connects direct.
func pickedHosts(t *testing.T, m *proxy.Manager, n int) []string {
	t.Helper()
	var hosts []string
	for range max(n, 1) {
		p, _, err := m.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if p.IsZero() {
			return hosts
		}
		hosts = append(hosts, p.HostPort())
	}
	slices.Sort(hosts)
	return hosts
}

func TestRunProxiesReadFails(t *testing.T) {
	s := proxyStore(t, "")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := runProxies(t.Context(), logtest.New(io.Discard), &config.Config{}, s)
	if err == nil || !strings.HasPrefix(err.Error(), "reading enabled proxies: ") || m != nil {
		t.Errorf("runProxies = %v, %v; want the read error", m, err)
	}
}

// proxyManager returns a manager that holds the proxies addrs.
func proxyManager(t *testing.T, addrs ...string) *proxy.Manager {
	t.Helper()
	proxies, _, err := proxy.Parse(strings.Join(addrs, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return proxy.NewManager(proxies)
}

const testProxyAddr = "203.0.113.1:8080"

// testProxy returns the proxy that testProxyAddr names.
func testProxy() proxy.Proxy {
	return proxy.Proxy{Host: "203.0.113.1", Port: "8080"}
}

// nextWait returns the time that Next of m waits on the fake clock.
func nextWait(t *testing.T, m *proxy.Manager) time.Duration {
	t.Helper()
	start := time.Now()
	if _, _, err := m.Next(t.Context()); err != nil {
		t.Fatalf("Next: %v", err)
	}
	return time.Since(start)
}

func TestCheckUpstream(t *testing.T) {
	t.Run("direct connection", func(t *testing.T) {
		var buf bytes.Buffer
		a := &attempt{logger: logtest.New(&buf)}
		if err := a.checkUpstream(t.Context(), nil); err != nil {
			t.Fatalf("checkUpstream: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected log:\n%s", buf.String())
		}
	})

	t.Run("reachable proxy", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		var buf bytes.Buffer
		a := &attempt{run: &runner{proxyMgr: proxyManager(t, ln.Addr().String())}, logger: logtest.New(&buf)}
		if a.p, _, err = a.run.proxyMgr.Next(t.Context()); err != nil {
			t.Fatalf("Next: %v", err)
		}
		fwd, _, err := startForwardProxy(t.Context(), a.logger, a.p)
		if err != nil {
			t.Fatalf("startForwardProxy: %v", err)
		}
		defer fwd.Stop()

		if err := a.checkUpstream(t.Context(), fwd); err != nil {
			t.Fatalf("checkUpstream: %v", err)
		}
		if strings.Contains(buf.String(), "proxy in cooldown") {
			t.Errorf("a reachable proxy failed the check:\n%s", buf.String())
		}
		if conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", fwd.Addr()); err != nil {
			t.Errorf("forward proxy stopped after a passed check: %v", err)
		} else {
			_ = conn.Close()
		}
	})

	t.Run("unreachable proxy", func(t *testing.T) {
		a, fwd, buf := unreachableProxy(t)

		err := a.checkUpstream(t.Context(), fwd)
		if !errors.Is(err, browser.ErrProxyConnection) || !errors.Is(err, errProxyCooled) {
			t.Fatalf("error = %v, want browser.ErrProxyConnection marked as cooled", err)
		}
		const wantText = "proxy connectivity check: proxy connection failed: "
		if !strings.HasPrefix(err.Error(), wantText) || len(err.Error()) == len(wantText) {
			t.Errorf("error = %q, want the prefix %q and the dial error", err, wantText)
		}
		if !strings.Contains(buf.String(), `msg="proxy in cooldown" reason="connectivity check" proxy=`+a.p.HostPort()+` error="`+wantText) ||
			!strings.Contains(buf.String(), "failures=1") {
			t.Errorf("log has no cooldown line for the proxy:\n%s", buf.String())
		}
		assertStopped(t, fwd)
		if strings.Contains(buf.String(), "upstream bytes by host") {
			t.Errorf("a failed check logged host bytes:\n%s", buf.String())
		}
	})

	t.Run("a cancelled check cools nothing", func(t *testing.T) {
		a, fwd, buf := unreachableProxy(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := a.checkUpstream(ctx, fwd)
		if !errors.Is(err, context.Canceled) || errors.Is(err, browser.ErrProxyConnection) || errors.Is(err, errProxyCooled) {
			t.Fatalf("error = %v, want context.Canceled with no proxy failure mark", err)
		}
		const wantText = "proxy connectivity check: dialing upstream proxy: "
		if !strings.HasPrefix(err.Error(), wantText) {
			t.Errorf("error = %q, want the prefix %q", err, wantText)
		}
		if strings.Contains(buf.String(), "level=WARN") {
			t.Errorf("a cancelled check logged a WARN:\n%s", buf.String())
		}
		assertStopped(t, fwd)
		if _, failures, started := a.run.proxyMgr.ReportFailure(a.p); failures != 1 || !started {
			t.Errorf("a later failure = %d, started %v; want 1, true: the cancelled check counted", failures, started)
		}
	})
}

// unreachableProxy returns an attempt whose one proxy refuses every connection,
// the started forward proxy of that proxy, and the log of the attempt.
func unreachableProxy(t *testing.T) (*attempt, *proxy.ForwardProxy, *bytes.Buffer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	var buf bytes.Buffer
	a := &attempt{run: &runner{proxyMgr: proxyManager(t, addr)}, logger: logtest.New(&buf)}
	if a.p, _, err = a.run.proxyMgr.Next(t.Context()); err != nil {
		t.Fatalf("Next: %v", err)
	}
	fwd, _, err := startForwardProxy(t.Context(), a.logger, a.p)
	if err != nil {
		t.Fatalf("startForwardProxy: %v", err)
	}
	return a, fwd, &buf
}

// assertStopped fails t when fwd still accepts a connection.
func assertStopped(t *testing.T, fwd *proxy.ForwardProxy) {
	t.Helper()
	if conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", fwd.Addr()); err == nil {
		_ = conn.Close()
		t.Errorf("forward proxy %s still listens after a failed check", fwd.Addr())
	}
}

func TestForwardProxyURL(t *testing.T) {
	if got := forwardProxyURL(nil); got != "" {
		t.Errorf("forwardProxyURL(nil) = %q, want empty", got)
	}
	fwd, _, err := startForwardProxy(t.Context(), slog.New(slog.DiscardHandler), proxy.Proxy{Host: "203.0.113.1", Port: "8080"})
	if err != nil {
		t.Fatalf("startForwardProxy: %v", err)
	}
	defer fwd.Stop()
	if got, want := forwardProxyURL(fwd), "http://"+fwd.Addr(); got != want {
		t.Errorf("forwardProxyURL = %q, want %q", got, want)
	}
}
