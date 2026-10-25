package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// defaultDialTimeout is the maximum time to wait when dialing the upstream proxy.
// The client gets 502 instead of a silent hang when the upstream is unreachable.
const defaultDialTimeout = 15 * time.Second

// checkTimeout bounds the upstream connectivity check, so a dead proxy fails
// before the slower Chrome navigation timeout.
const checkTimeout = 2 * time.Second

// maxPoolSize is the maximum number of idle connections kept in the upstream pool.
const maxPoolSize = 4

// allowedDomains returns the only domain suffixes the forward proxy will allow.
// Requests to any other domain are rejected with 403, saving bandwidth.
func allowedDomains() []string {
	return []string{
		"jagex.com",
		"cloudflare.com",
		"ipify.org",
	}
}

// byteCount holds the bytes that the upstream connections of one ForwardProxy
// or one tunnel send and receive.
type byteCount struct {
	sent, received atomic.Int64
}

// hostBytes holds the tunnel bytes of one CONNECT host. connects counts the
// CONNECTs that reached the upstream open, failed ones too, not the tunnels established.
type hostBytes struct {
	connects       int
	sent, received int64
}

// countingConn adds the bytes of each Read and Write to count.
type countingConn struct {
	net.Conn

	count *byteCount
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.count.received.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.count.sent.Add(int64(n))
	return n, err
}

// upstreamPool manages idle TCP connections to an HTTP upstream proxy to avoid
// repeated TCP handshakes for sequential requests through the same proxy. It
// also holds the connection of the connectivity check for the first CONNECT.
type upstreamPool struct {
	mu    sync.Mutex
	idle  []net.Conn
	addr  string
	count *byteCount

	dialFn func(ctx context.Context, addr string) (net.Conn, error) // test override
}

// Get returns an idle connection from the pool, or dials a new one.
// Stale connections (peer closed) are detected and discarded.
func (p *upstreamPool) Get(ctx context.Context, dialTimeout time.Duration) (net.Conn, error) {
	p.mu.Lock()
	for len(p.idle) > 0 {
		conn := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		p.mu.Unlock()
		if isConnAlive(conn) {
			return conn, nil
		}
		_ = conn.Close()
		p.mu.Lock()
	}
	p.mu.Unlock()
	var conn net.Conn
	var err error
	if p.dialFn != nil {
		conn, err = p.dialFn(ctx, p.addr)
	} else {
		conn, err = (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", p.addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dialing upstream proxy: %w", err)
	}
	// Wrap at the dial only, so a reused connection counts once.
	return &countingConn{Conn: conn, count: p.count}, nil
}

// Put returns a connection to the pool. If the pool is full, the connection is closed.
func (p *upstreamPool) Put(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.idle) >= maxPoolSize {
		_ = conn.Close()
		return
	}
	p.idle = append(p.idle, conn)
}

// CloseAll closes all idle connections and empties the pool.
func (p *upstreamPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.idle {
		_ = conn.Close()
	}
	p.idle = nil
}

// isConnAlive checks whether an idle connection is still usable by doing a
// short-deadline read. Only a timeout (deadline exceeded) means the connection
// is idle and alive. Data on an idle connection, or any other error (EOF, RST),
// means not alive: a nil error is not a net.Error.
func isConnAlive(conn net.Conn) bool {
	_ = conn.SetReadDeadline(time.Now().Add(time.Millisecond))
	var buf [1]byte
	_, err := conn.Read(buf[:])
	_ = conn.SetReadDeadline(time.Time{})
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

// ForwardProxy is a local HTTP proxy that chains the requests of one client,
// Chrome or the HTTP client, to an upstream proxy. The upstream speaks HTTP
// CONNECT or SOCKS5. ForwardProxy counts the bytes on its upstream connections,
// in total and for each CONNECT host.
//
// Every connection passes the domain allowlist, which blocks
// third-party traffic (analytics, tracking, etc.) at the network level.
// The upstream gets the credentials when the proxy line has them. The client
// connects on localhost without credentials, so Chrome needs no CDP Fetch auth,
// which bypasses Chrome's disk cache.
type ForwardProxy struct {
	listener      net.Listener
	proxyAuth     string        // HTTP upstream: "Basic <base64>" header value, empty if no auth
	pool          *upstreamPool // HTTP upstream only
	socks         xproxy.ContextDialer
	socksAddr     string // SOCKS5 upstream host:port, for CheckUpstream
	dialTimeout   time.Duration
	logger        *slog.Logger
	wg            sync.WaitGroup
	upstreamBytes byteCount
	endLife       context.CancelFunc // ends the context of the handlers, the dials and the live tunnels

	hostsMu sync.Mutex
	hosts   map[string]hostBytes // keyed by the CONNECT host without its port
}

// NewForwardProxy creates and starts a forward proxy listening on localhost.
// upstream is an http or socks5 proxy, and its credentials may be empty.
// The idle connection pool serves only an http upstream, because a SOCKS5
// connection is bound to one target. ctx bounds only the listen call. Stop
// ends each dial in flight.
func NewForwardProxy(ctx context.Context, upstream Proxy, logger *slog.Logger) (*ForwardProxy, error) {
	p := &ForwardProxy{
		dialTimeout: defaultDialTimeout,
		logger:      logger.With("component", "forward-proxy"),
		hosts:       make(map[string]hostBytes),
	}
	addr := upstream.addr()
	switch s := upstream.scheme(); s {
	case "http":
		p.pool = &upstreamPool{addr: addr, count: &p.upstreamBytes}
		if upstream.Username != "" {
			p.proxyAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(upstream.Username+":"+upstream.Password))
		}
	case "socks5":
		var auth *xproxy.Auth
		if upstream.Username != "" {
			auth = &xproxy.Auth{User: upstream.Username, Password: upstream.Password}
		}
		// SOCKS5 returns no error and always a ContextDialer, whose handshake runs under ctx.
		// Limitation: the dialer refuses a username or password over 255 bytes, and a reply with a non-zero reserved byte.
		d, _ := xproxy.SOCKS5("tcp", addr, auth, nil)
		p.socks, _ = d.(xproxy.ContextDialer)
		p.socksAddr = addr
	default:
		return nil, fmt.Errorf("unsupported upstream scheme %q", s)
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening: %w", err)
	}
	p.listener = listener

	// The proxy outlives the end of ctx, so the work past the password POST can still dial.
	life, endLife := context.WithCancel(context.WithoutCancel(ctx))
	p.endLife = endLife
	p.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // listener closed
			}
			p.wg.Go(func() {
				p.handleConn(life, conn)
			})
		}
	})

	p.logger.Debug("forward proxy started", "addr", listener.Addr().String())
	return p, nil
}

// Addr returns the listener address (e.g. "127.0.0.1:12345").
func (p *ForwardProxy) Addr() string {
	return p.listener.Addr().String()
}

// CheckUpstream checks within checkTimeout that the upstream accepts a TCP
// connection. For an HTTP upstream it dials through the pool and leaves the
// connection idle there, so the first CONNECT reuses it. A SOCKS5 connection is
// bound to one target, so the SOCKS5 check closes its connection.
// Limitation: the upstream can close the idle connection before the first
// CONNECT. When the close arrived first, the pool dials fresh. Otherwise that
// CONNECT fails toward a 502.
func (p *ForwardProxy) CheckUpstream(ctx context.Context) error {
	if p.pool != nil {
		conn, err := p.pool.Get(ctx, checkTimeout)
		if err != nil {
			return err
		}
		p.pool.Put(conn)
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(checkCtx, "tcp", p.socksAddr)
	if err != nil {
		return fmt.Errorf("dialing SOCKS5 upstream: %w", err)
	}
	_ = conn.Close()
	return nil
}

// Bytes returns the bytes sent to and received from the upstream proxy. The
// totals are final after Stop returns.
func (p *ForwardProxy) Bytes() (sent, received int64) {
	return p.upstreamBytes.sent.Load(), p.upstreamBytes.received.Load()
}

// Stop ends the context of the handlers. That ends each dial and each CONNECT
// exchange in flight. It also closes both ends of each live tunnel. Stop
// closes the listener, waits for the handlers and drains the connection pool.
// Then it logs the tunnel bytes of each CONNECT host at DEBUG, once.
// Limitation: plain HTTP relay bytes count in Bytes but in no host line.
func (p *ForwardProxy) Stop() {
	p.endLife()
	_ = p.listener.Close()
	p.wg.Wait()
	if p.pool != nil {
		p.pool.CloseAll()
	}

	p.hostsMu.Lock()
	hosts := p.hosts
	p.hosts = nil
	p.hostsMu.Unlock()
	for _, host := range slices.Sorted(maps.Keys(hosts)) {
		h := hosts[host]
		p.logger.Debug("upstream bytes by host", "host", host, "connects", h.connects, "sent", h.sent, "received", h.received)
	}
}

func (p *ForwardProxy) handleConn(ctx context.Context, clientConn net.Conn) {
	defer func() { _ = clientConn.Close() }()

	reader := bufio.NewReader(clientConn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}

	// Extract host for domain filtering.
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !isAllowedDomain(host) {
		p.logger.Debug("blocked by domain filter", "host", host)
		if req.Method == "CONNECT" {
			_, _ = fmt.Fprint(clientConn, "HTTP/1.1 403 Forbidden\r\n\r\n")
		} else {
			_, _ = fmt.Fprint(clientConn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		}
		return
	}

	switch {
	case req.Method == "CONNECT":
		p.handleCONNECT(ctx, clientConn, req, host)
	case p.socks != nil:
		p.handleSOCKS5HTTP(ctx, clientConn, req, host)
	default:
		p.handleHTTP(ctx, clientConn, req, host)
	}
}

// failUpstream logs one upstream failure at DEBUG and sends status to the
// client. It is the only writer of an upstream failure reply.
func (p *ForwardProxy) failUpstream(clientConn net.Conn, host string, status int, err error) {
	p.logger.Debug("upstream failed", "host", host, "status", status, "error", err)
	_, _ = fmt.Fprintf(clientConn, "HTTP/1.1 %d %s\r\n\r\n", status, http.StatusText(status))
}

func (p *ForwardProxy) handleCONNECT(ctx context.Context, clientConn net.Conn, req *http.Request, host string) {
	var tunnel byteCount
	defer p.addHostBytes(host, &tunnel)

	open := p.openHTTPTunnel
	if p.socks != nil {
		open = p.openSOCKS5Tunnel
	}
	upConn, upRead, ok := open(ctx, clientConn, req, host, &tunnel)
	if !ok {
		return // open has sent the error status to the client
	}
	defer func() { _ = upConn.Close() }() // CONNECT tunnels cannot be returned to the pool
	// Stop ends ctx, and the close ends both copies of splice, also when the client stays open.
	defer context.AfterFunc(ctx, func() {
		_ = clientConn.Close()
		_ = upConn.Close()
	})()

	// Tell the client the tunnel is established.
	_, _ = fmt.Fprint(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	splice(clientConn, upConn, upRead)
}

// addHostBytes adds one tunnel to the count of its CONNECT host.
func (p *ForwardProxy) addHostBytes(host string, tunnel *byteCount) {
	p.hostsMu.Lock()
	defer p.hostsMu.Unlock()
	h := p.hosts[host]
	h.connects++
	h.sent += tunnel.sent.Load()
	h.received += tunnel.received.Load()
	p.hosts[host] = h
}

// openHTTPTunnel sends CONNECT to the HTTP upstream and counts the tunnel
// bytes, the CONNECT exchange included, in tunnel. On failure it sends the
// error status to the client, closes the upstream connection and returns false.
func (p *ForwardProxy) openHTTPTunnel(ctx context.Context, clientConn net.Conn, req *http.Request, host string, tunnel *byteCount) (net.Conn, io.Reader, bool) {
	// Get a connection to the upstream proxy (pooled or new).
	pooled, err := p.pool.Get(ctx, p.dialTimeout)
	if err != nil {
		p.failUpstream(clientConn, host, http.StatusBadGateway, err)
		return nil, nil, false
	}
	// A pooled connection can carry a CONNECT after a plain request, so the host count is per tunnel.
	upConn := &countingConn{Conn: pooled, count: tunnel}
	// Stop ends ctx, and the close ends a CONNECT exchange that the upstream does not answer.
	defer context.AfterFunc(ctx, func() { _ = upConn.Close() })()

	// Send CONNECT to upstream with auth.
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", req.Host, req.Host)
	if p.proxyAuth != "" {
		connectReq += "Proxy-Authorization: " + p.proxyAuth + "\r\n"
	}
	connectReq += "\r\n"

	if _, err := io.WriteString(upConn, connectReq); err != nil {
		_ = upConn.Close()
		p.failUpstream(clientConn, host, http.StatusBadGateway, fmt.Errorf("writing CONNECT to the upstream: %w", err))
		return nil, nil, false
	}

	// Read upstream response.
	upReader := bufio.NewReader(upConn)
	resp, err := http.ReadResponse(upReader, req)
	if err != nil {
		_ = upConn.Close()
		p.failUpstream(clientConn, host, http.StatusBadGateway, fmt.Errorf("reading the CONNECT response: %w", err))
		return nil, nil, false
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		_ = upConn.Close()
		p.failUpstream(clientConn, host, resp.StatusCode, errors.New("the upstream refused the CONNECT"))
		return nil, nil, false
	}
	// upReader, not upConn: it holds any bytes read ahead of the tunnel.
	return upConn, upReader, true
}

// openSOCKS5Tunnel dials req.Host through the SOCKS5 upstream and counts the
// tunnel bytes in tunnel. A SOCKS5 failure has no HTTP status to relay, so the
// client gets 502.
func (p *ForwardProxy) openSOCKS5Tunnel(ctx context.Context, clientConn net.Conn, req *http.Request, host string, tunnel *byteCount) (net.Conn, io.Reader, bool) {
	conn, err := p.dialSOCKS5(ctx, req.Host)
	if err != nil {
		p.failUpstream(clientConn, host, http.StatusBadGateway, err)
		return nil, nil, false
	}
	upConn := &countingConn{Conn: conn, count: tunnel}
	return upConn, upConn, true
}

// dialSOCKS5 dials addr through the SOCKS5 upstream and counts the bytes of
// the tunnel.
// Limitation: the dialer runs the SOCKS5 handshake before the wrap, so the
// handshake bytes are not counted. The count is low by those bytes.
// Limitation: after Stop, the error of a handshake in flight reads i/o timeout, not context canceled.
func (p *ForwardProxy) dialSOCKS5(ctx context.Context, addr string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, p.dialTimeout)
	defer cancel()
	conn, err := p.socks.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing through the SOCKS5 upstream: %w", err)
	}
	return &countingConn{Conn: conn, count: &p.upstreamBytes}, nil
}

// splice copies bytes both ways between the client and the upstream. upRead is
// the read side of upConn.
func splice(clientConn, upConn net.Conn, upRead io.Reader) {
	// When one direction ends, close the other connection to unblock the peer copy.
	// Otherwise wg.Wait() hangs when the upstream is slow to close after the client exits.
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = io.Copy(upConn, clientConn)
		_ = upConn.Close()
	})
	wg.Go(func() {
		_, _ = io.Copy(clientConn, upRead)
		_ = clientConn.Close()
	})
	wg.Wait()
}

func (p *ForwardProxy) handleHTTP(ctx context.Context, clientConn net.Conn, req *http.Request, host string) {
	// Get a connection to the upstream proxy (pooled or new).
	upConn, err := p.pool.Get(ctx, p.dialTimeout)
	if err != nil {
		p.failUpstream(clientConn, host, http.StatusBadGateway, err)
		return
	}

	// Add proxy auth header.
	if p.proxyAuth != "" {
		req.Header.Set("Proxy-Authorization", p.proxyAuth)
	}

	if !p.relayRequest(clientConn, upConn, req, host) {
		_ = upConn.Close()
		return
	}
	p.pool.Put(upConn)
}

// handleSOCKS5HTTP sends one plain HTTP request through a SOCKS5 tunnel to
// the target. The tunnel is bound to that target, so it never goes to the pool.
func (p *ForwardProxy) handleSOCKS5HTTP(ctx context.Context, clientConn net.Conn, req *http.Request, host string) {
	addr := req.Host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "80")
	}
	upConn, err := p.dialSOCKS5(ctx, addr)
	if err != nil {
		p.failUpstream(clientConn, host, http.StatusBadGateway, err)
		return
	}
	defer func() { _ = upConn.Close() }()
	p.relayRequest(clientConn, upConn, req, host)
}

// relayRequest writes req to upConn in origin form and copies the response to
// the client. A failed write or read sends 502 to the client. It reports
// whether upConn can carry another request.
// Limitation: an HTTP upstream also gets origin form, which RFC 7230 reserves
// for origin servers. No production request is plain HTTP.
func (p *ForwardProxy) relayRequest(clientConn, upConn net.Conn, req *http.Request, host string) bool {
	if err := req.Write(upConn); err != nil {
		p.failUpstream(clientConn, host, http.StatusBadGateway, fmt.Errorf("writing the request to the upstream: %w", err))
		return false
	}

	// Parse the full response so we know when it ends, enabling connection reuse.
	br := bufio.NewReader(upConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		p.failUpstream(clientConn, host, http.StatusBadGateway, fmt.Errorf("reading the upstream response: %w", err))
		return false
	}

	writeErr := resp.Write(clientConn)
	_ = resp.Body.Close()

	// The connection is reusable if the response allows keep-alive and no errors occurred.
	if writeErr != nil || resp.Close || br.Buffered() > 0 {
		return false
	}
	return true
}

func isAllowedDomain(host string) bool {
	host = strings.ToLower(host)
	for _, allowed := range allowedDomains() {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}
