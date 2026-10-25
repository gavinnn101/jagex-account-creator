package proxy

import "net"

// Proxy represents a parsed proxy line. Username and Password are empty for a
// line without credentials.
type Proxy struct {
	Scheme   string // "http" or "socks5". Defaults to "http".
	Host     string
	Port     string
	Username string
	Password string
}

// IsZero returns true if this is a zero-value proxy (no proxy configured).
func (p Proxy) IsZero() bool {
	return p.Host == ""
}

// scheme returns the proxy's scheme, defaulting to "http" if unset.
func (p Proxy) scheme() string {
	if p.Scheme == "" {
		return "http"
	}
	return p.Scheme
}

// addr returns the host:port of the proxy.
func (p Proxy) addr() string {
	return net.JoinHostPort(p.Host, p.Port)
}

// HostPort returns the display and identity form of the proxy, with no
// credentials. Logs, the health key and the account record use it. For HTTP
// proxies this is "host:port". For SOCKS5 proxies it includes the scheme
// prefix ("socks5://host:port"), which distinguishes it from an HTTP proxy at
// the same host:port. Returns an empty string if the proxy is zero-value.
func (p Proxy) HostPort() string {
	if p.IsZero() {
		return ""
	}
	hp := p.addr()
	if s := p.scheme(); s != "http" {
		return s + "://" + hp
	}
	return hp
}

// Line returns the proxy file line of the proxy, credentials included, so
// Parse of the line gives the proxy back. It returns "" for a zero-value proxy.
func (p Proxy) Line() string {
	if p.Username == "" {
		return p.HostPort()
	}
	return p.HostPort() + ":" + p.Username + ":" + p.Password
}
