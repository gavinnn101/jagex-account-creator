// Package proxytest provides a fake SOCKS5 server for tests.
package proxytest

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"

	"jagex_account_creator/internal/waittest"
)

// Record is what the fake saw in one handshake.
type Record struct {
	Methods    []byte
	User, Pass string
	Cmd        byte // 1 is CONNECT, 3 is UDP ASSOCIATE
	Atyp       byte
	Host       string
	Port       uint16
	RequestURI string // ServeHTTP only: the request target sent through the tunnel
}

// Config sets how the fake answers.
type Config struct {
	AuthStatus byte // RFC 1929 status: 0 accepts, any other value rejects
	Reply      byte // RFC 1928 reply to the request: 0 succeeds, any other value refuses
	ServeHTTP  bool // after a CONNECT, answer one plain HTTP request with "OK" instead of an echo
	NoAuth     bool // select "no authentication" and skip the RFC 1929 exchange
}

// SOCKS5 is a SOCKS5 server (RFC 1928). It requires username/password auth
// (RFC 1929) unless Config.NoAuth is set. After a successful request it echoes
// bytes, or serves HTTP when Config.ServeHTTP is set.
type SOCKS5 struct {
	ln  net.Listener
	cfg Config
	wg  sync.WaitGroup

	mu       sync.Mutex
	accepted int
	conns    []net.Conn
	recs     []Record
}

// NewSOCKS5 starts a fake on a loopback port. The test cleanup stops it.
func NewSOCKS5(tb testing.TB, cfg Config) *SOCKS5 {
	tb.Helper()
	ln, err := (&net.ListenConfig{}).Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	f := &SOCKS5{ln: ln, cfg: cfg}
	f.wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.accepted++
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			f.wg.Go(func() { f.handle(c) })
		}
	})
	// Close every accepted conn too, so a handler blocked in the echo cannot hang the test.
	tb.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		for _, c := range f.conns {
			_ = c.Close()
		}
		f.mu.Unlock()
		waittest.Wait(tb, &f.wg)
	})
	return f
}

// Addr returns the host:port the fake listens on.
func (f *SOCKS5) Addr() string {
	return f.ln.Addr().String()
}

// Snapshot returns the count of accepted connections and a copy of the records.
func (f *SOCKS5) Snapshot() (accepted int, recs []Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accepted, append([]Record(nil), f.recs...)
}

func (f *SOCKS5) add(r Record) {
	f.mu.Lock()
	f.recs = append(f.recs, r)
	f.mu.Unlock()
}

func (f *SOCKS5) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	r, ok := f.handshake(c, br)
	if !ok {
		return
	}
	if !f.cfg.ServeHTTP {
		_, _ = io.Copy(c, br)
		return
	}
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	r.RequestURI = req.RequestURI
	f.add(r)
	_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")
}

// handshake runs the method, auth and request exchange. It records the
// handshake before its last reply, so the record exists when the client sees
// the result. A served HTTP request is recorded later, with its target.
func (f *SOCKS5) handshake(c net.Conn, br *bufio.Reader) (Record, bool) {
	rd := &reader{br: br}
	var r Record
	rd.version(5)
	r.Methods = rd.field()
	if rd.err != nil {
		return r, false
	}
	if f.cfg.NoAuth {
		_, _ = c.Write([]byte{5, 0}) // no authentication
	} else if !f.auth(c, rd, &r) {
		return r, false
	}

	head := rd.bytes(4) // VER CMD RSV ATYP
	r.Cmd, r.Atyp = head[1], head[3]
	r.Host = rd.addr(r.Atyp)
	r.Port = binary.BigEndian.Uint16(rd.bytes(2))
	if rd.err != nil {
		return r, false
	}
	if f.cfg.Reply != 0 || !f.cfg.ServeHTTP {
		f.add(r)
	}
	_, _ = c.Write([]byte{5, f.cfg.Reply, 0, 1, 0, 0, 0, 0, 0, 0})
	return r, f.cfg.Reply == 0
}

// auth selects username/password and runs the RFC 1929 exchange into r. It
// reports whether the fake accepted the credentials.
func (f *SOCKS5) auth(c net.Conn, rd *reader, r *Record) bool {
	_, _ = c.Write([]byte{5, 2}) // username/password
	rd.version(1)
	r.User = string(rd.field())
	r.Pass = string(rd.field())
	if rd.err != nil {
		return false
	}
	if f.cfg.AuthStatus != 0 {
		f.add(*r)
		_, _ = c.Write([]byte{1, f.cfg.AuthStatus})
		return false
	}
	_, _ = c.Write([]byte{1, 0})
	return true
}

// reader reads a handshake. After its first error, each read returns zero bytes.
type reader struct {
	br  *bufio.Reader
	err error
}

func (r *reader) bytes(n int) []byte {
	b := make([]byte, n)
	if r.err == nil {
		_, r.err = io.ReadFull(r.br, b)
	}
	return b
}

// field reads a length byte and that many bytes.
func (r *reader) field() []byte {
	return r.bytes(int(r.bytes(1)[0]))
}

func (r *reader) version(want byte) {
	if v := r.bytes(1)[0]; r.err == nil && v != want {
		r.err = fmt.Errorf("version %d, want %d", v, want)
	}
}

// addr reads the target address of a request in each ATYP that RFC 1928 defines.
func (r *reader) addr(atyp byte) string {
	switch atyp {
	case 1:
		return net.IP(r.bytes(4)).String()
	case 3:
		return string(r.field())
	case 4:
		return net.IP(r.bytes(16)).String()
	default:
		r.err = fmt.Errorf("address type %d", atyp)
		return ""
	}
}
