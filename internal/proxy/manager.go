// Package proxy loads proxies and picks one for each attempt, and forwards the traffic of Chrome or the HTTP client to the upstream proxy.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// healthCooldownInitial is the cooldown after a proxy's first failure.
	// Each subsequent failure doubles the cooldown, up to healthCooldownMax.
	healthCooldownInitial = 5 * time.Minute

	// healthCooldownMax caps the exponential cooldown.
	healthCooldownMax = 30 * time.Minute
)

// proxyHealth tracks the failure state of a single proxy.
type proxyHealth struct {
	failures      int
	cooldownUntil time.Time
}

// Manager gives each attempt the healthy proxy with the fewest attempts in
// flight. It is safe for concurrent use.
// ReportFailure puts the proxy in a cooldown that doubles with each counted
// failure, and Next skips a proxy during its cooldown. A failure reported
// during a cooldown that still runs is not counted.
type Manager struct {
	proxies []Proxy

	// mu guards the three fields below.
	mu       sync.Mutex
	next     int                    // the slot where the scan of the next pick starts
	inFlight []int                  // the attempts in flight on each slot
	health   map[string]proxyHealth // key: HostPort
}

// newManager returns a Manager of proxies with no attempt in flight, whose scan starts at the first proxy.
func newManager(proxies []Proxy) *Manager {
	return &Manager{proxies: proxies, inFlight: make([]int, len(proxies))}
}

// NewManager returns a Manager of proxies whose scan starts at a random proxy.
// With no proxies, each attempt connects direct.
func NewManager(proxies []Proxy) *Manager {
	m := newManager(proxies)
	if len(proxies) > 0 {
		m.next = rand.IntN(len(proxies))
	}
	return m
}

// Next returns the healthy proxy with the fewest attempts in flight, and a
// release func. A tie goes to the first proxy in rotation order from the slot
// after the last pick. A proxy is unhealthy during the cooldown that
// ReportFailure starts.
// The caller calls release when the attempt's use of the proxy ends. A second
// call does nothing.
// When every proxy is unhealthy, Next waits until the earliest cooldown ends,
// or until ctx ends and then returns the ctx error and a nil release.
// With no proxies loaded, it returns a zero-value Proxy (direct connection)
// and a release that does nothing, at once.
func (m *Manager) Next(ctx context.Context) (Proxy, func(), error) {
	if len(m.proxies) == 0 {
		return Proxy{}, func() {}, nil
	}
	for {
		slot, wait, ok := m.nextHealthy()
		if ok {
			return m.proxies[slot], sync.OnceFunc(func() { m.release(slot) }), nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Proxy{}, nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// nextHealthy picks the healthy slot with the fewest attempts in flight and
// counts one more attempt on it. When none is healthy, it returns the time
// until the earliest cooldown ends.
func (m *Manager) nextHealthy() (slot int, wait time.Duration, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := len(m.proxies)
	now := time.Now()
	// No cooldown is longer than healthCooldownMax.
	wait = healthCooldownMax
	for k := range n {
		i := (m.next + k) % n
		until := m.health[m.proxies[i].HostPort()].cooldownUntil
		if now.Before(until) {
			wait = min(wait, until.Sub(now))
			continue
		}
		// Strict less keeps a tie on the first slot in rotation order.
		if !ok || m.inFlight[i] < m.inFlight[slot] {
			slot, ok = i, true
		}
	}
	if !ok {
		return 0, wait, false
	}
	m.inFlight[slot]++
	m.next = (slot + 1) % n
	return slot, 0, true
}

// release counts one attempt less on slot.
func (m *Manager) release(slot int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight[slot]--
}

// ReportFailure counts a failure of p and starts a cooldown that doubles with
// each counted failure. It returns the cooldown, the failure count and started
// true. A report during a cooldown that still runs changes nothing and returns
// started false, because the attempts in flight on p see one incident.
func (m *Manager) ReportFailure(p Proxy) (cooldown time.Duration, failures int, started bool) {
	key := p.HostPort()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.health == nil {
		m.health = make(map[string]proxyHealth)
	}
	h := m.health[key]
	now := time.Now()
	if now.Before(h.cooldownUntil) {
		return 0, 0, false
	}
	h.failures++
	cooldown = min(healthCooldownInitial<<uint(min(h.failures-1, 3)), healthCooldownMax)
	h.cooldownUntil = now.Add(cooldown)
	m.health[key] = h
	return cooldown, h.failures, true
}

// ReportSuccess clears the failure state of a proxy whose cooldown is over,
// so that its next failure gets the base cooldown. It keeps a cooldown that
// still runs: an attempt that took the proxy before a failure can succeed after it.
func (m *Manager) ReportSuccess(p Proxy) {
	key := p.HostPort()

	m.mu.Lock()
	defer m.mu.Unlock()

	if time.Now().Before(m.health[key].cooldownUntil) {
		return
	}
	delete(m.health, key)
}

// LineError is the error of a bad line of the proxy file text. It names the
// line by its number only, because the line text holds credentials.
type LineError struct {
	Line int // 1-based
	Err  error
}

func (e *LineError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

func (e *LineError) Unwrap() error {
	return e.Err
}

// Parse parses the proxy file text: one proxy per line in the format
//
//	[scheme://]host:port[:username:password]
//
// The scheme is "http", "socks5" or "socks5h". The parser reads "socks5h" as
// "socks5". A line without a scheme uses "http".
// Empty lines and lines starting with '#' are skipped. Two lines with one
// HostPort are one proxy: it keeps the place of the first line and the
// credentials of the last, and repeated counts each earlier line.
// A bad line stops the parse with a *LineError.
func Parse(text string) (proxies []Proxy, repeated int, err error) {
	slot := make(map[string]int) // key: HostPort
	lineNum := 0
	for line := range strings.Lines(text) {
		lineNum++
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := ParseLine(line)
		if err != nil {
			return nil, 0, &LineError{Line: lineNum, Err: err}
		}
		if i, ok := slot[p.HostPort()]; ok {
			proxies[i] = p
			repeated++
			continue
		}
		slot[p.HostPort()] = len(proxies)
		proxies = append(proxies, p)
	}
	return proxies, repeated, nil
}

// hostChars are the characters of an IPv4 address or a host name.
const hostChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_"

// ParseLine parses one proxy line in the format that Parse documents. It also
// reads a HostPort back. It checks each field, so a parsed Proxy is never zero.
// Its errors hold no line text, because the line text holds credentials.
func ParseLine(line string) (Proxy, error) {
	scheme := "http"
	rest := line

	// A prefix that holds ':' is host:port text, because a password can hold "://".
	if before, after, ok := strings.Cut(line, "://"); ok && !strings.Contains(before, ":") {
		scheme = before
		rest = after
	}
	// NewForwardProxy speaks only http and SOCKS5 to the upstream.
	switch scheme {
	case "http", "socks5":
	case "socks5h":
		// The forward proxy's SOCKS5 dialer sends the host name, so socks5h is socks5.
		scheme = "socks5"
	default:
		return Proxy{}, errors.New("unsupported scheme: use http, socks5 or socks5h")
	}

	parts := strings.SplitN(rest, ":", 4)
	if len(parts) != 2 && len(parts) != 4 {
		return Proxy{}, fmt.Errorf("expected host:port or host:port:user:pass, got %d parts", len(parts))
	}
	p := Proxy{Scheme: scheme, Host: parts[0], Port: parts[1]}
	if len(parts) == 4 {
		p.Username, p.Password = parts[2], parts[3]
		if p.Username == "" {
			return Proxy{}, errors.New("empty user name")
		}
	}
	// Limitation: a Unicode host does not load, so the operator writes punycode.
	// An IPv6 host does not load.
	if p.Host == "" || strings.Trim(p.Host, hostChars) != "" {
		return Proxy{}, errors.New("host must be an IPv4 address or a host name")
	}
	if n, err := strconv.ParseUint(p.Port, 10, 16); err != nil || n == 0 {
		return Proxy{}, errors.New("port must be a number from 1 to 65535")
	}
	return p, nil
}
