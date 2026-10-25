package jagexapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/proxy"
)

// connectReply is the answer of the fake upstream to every CONNECT.
const connectReply = "HTTP/1.1 200 Connection Established\r\n\r\n"

// tunnelSeen is what the fake upstream read on one connection: the CONNECT
// request with its headers, and the first record that the client sent in the tunnel.
type tunnelSeen struct {
	connect    string
	recordType byte
	recordLen  int
}

// connectUpstream is a fake HTTP upstream proxy. It accepts every CONNECT,
// reads one TLS record from the tunnel and closes. No byte leaves the machine.
type connectUpstream struct {
	ln   net.Listener
	seen chan tunnelSeen

	mu      sync.Mutex
	replies int
}

func newConnectUpstream(t *testing.T) *connectUpstream {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &connectUpstream{ln: ln, seen: make(chan tunnelSeen, 8)}
	t.Cleanup(func() { _ = ln.Close() })
	go u.serve()
	return u
}

func (u *connectUpstream) serve() {
	for {
		c, err := u.ln.Accept()
		if err != nil {
			return
		}
		go u.handle(c)
	}
}

func (u *connectUpstream) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	var head strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		head.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(c, connectReply); err != nil {
		return
	}
	u.mu.Lock()
	u.replies++
	u.mu.Unlock()

	// A TLS record header is 5 bytes: the type, the version and the body length.
	var hdr [5]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint16(hdr[3:]))
	if _, err := io.ReadFull(br, make([]byte, n)); err != nil {
		return
	}
	u.seen <- tunnelSeen{connect: head.String(), recordType: hdr[0], recordLen: len(hdr) + n}
}

// The HTTP phase goes through its own forward proxy, so that proxy counts
// the CONNECT exchange and the TLS bytes of the tunnel.
func TestClientThroughForwardProxyIsCounted(t *testing.T) {
	up := newConnectUpstream(t)
	host, port, err := net.SplitHostPort(up.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fwd, err := proxy.NewForwardProxy(t.Context(), proxy.Proxy{Host: host, Port: port, Username: "u", Password: "p"}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Stop()

	c := NewClient(slog.New(slog.DiscardHandler), "test", testHints(), "http://"+fwd.Addr())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	resp, err := c.Do(ctx, &client.Request{Method: "GET", URL: "https://account.jagex.com/"})
	if err == nil {
		_ = resp.Close()
		t.Fatal("Do succeeded through an upstream that closes after the first TLS record")
	}
	c.Close()

	var seen tunnelSeen
	select {
	case seen = <-up.seen:
	case <-time.After(5 * time.Second):
		t.Fatalf("the upstream saw no TLS record; Do error: %v", err)
	}
	const wantConnect = "CONNECT account.jagex.com:443 HTTP/1.1\r\nHost: account.jagex.com:443\r\nProxy-Authorization: Basic dTpw\r\n\r\n"
	if seen.connect != wantConnect {
		t.Errorf("CONNECT = %q, want %q", seen.connect, wantConnect)
	}
	if seen.recordType != 0x16 {
		t.Errorf("first tunnel record type = %#x, want 0x16 (TLS handshake)", seen.recordType)
	}

	fwd.Stop()
	sent, received := fwd.Bytes()
	if minSent := int64(len(wantConnect) + seen.recordLen); sent < minSent {
		t.Errorf("sent = %d, want at least %d: the CONNECT and the first TLS record", sent, minSent)
	}
	up.mu.Lock()
	replies := up.replies
	up.mu.Unlock()
	if want := int64(replies * len(connectReply)); received != want {
		t.Errorf("received = %d, want %d: %d CONNECT replies", received, want, replies)
	}
}
