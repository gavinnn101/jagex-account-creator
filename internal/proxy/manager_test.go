package proxy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// mustNext picks a proxy of m and holds it.
func mustNext(t *testing.T, m *Manager) Proxy {
	t.Helper()
	p, _ := mustPick(t, m)
	return p
}

// mustPick picks a proxy of m and returns it with its release.
func mustPick(t *testing.T, m *Manager) (Proxy, func()) {
	t.Helper()
	p, release, err := m.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return p, release
}

// holdHosts picks n proxies of m, holds each one and returns their hosts.
func holdHosts(t *testing.T, m *Manager, n int) []string {
	t.Helper()
	hosts := make([]string, 0, n)
	for range n {
		hosts = append(hosts, mustNext(t, m).Host)
	}
	return hosts
}

// releasedHosts picks n proxies of m in turn, releases each one before the next
// pick and returns their hosts.
func releasedHosts(t *testing.T, m *Manager, n int) []string {
	t.Helper()
	hosts := make([]string, 0, n)
	for range n {
		p, release := mustPick(t, m)
		hosts = append(hosts, p.Host)
		release()
	}
	return hosts
}

// threeProxies returns a manager of the proxies a, b and c whose scan starts at a.
func threeProxies() *Manager {
	return newManager([]Proxy{{Host: "a", Port: "1"}, {Host: "b", Port: "2"}, {Host: "c", Port: "3"}})
}

func TestNextPicksLeastLoaded(t *testing.T) {
	m := threeProxies()
	mustNext(t, m) // a stays in flight.
	// Rotation alone gives b, c, a, b.
	if got, want := releasedHosts(t, m, 4), []string{"b", "c", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("picks = %v, want %v: a carries one attempt more than b and c", got, want)
	}
}

func TestNextPickAndReleaseKeepsRotation(t *testing.T) {
	if got, want := releasedHosts(t, threeProxies(), 4), []string{"a", "b", "c", "a"}; !slices.Equal(got, want) {
		t.Errorf("picks = %v, want %v: a tie goes to the slot after the last pick", got, want)
	}
}

func TestNextReleaseLowersLoadOnce(t *testing.T) {
	m := newManager([]Proxy{{Host: "a", Port: "1"}, {Host: "b", Port: "2"}})
	_, releaseA := mustPick(t, m)
	mustNext(t, m) // b stays in flight.
	releaseA()
	releaseA()
	if want := []int{0, 1}; !slices.Equal(m.inFlight, want) {
		t.Errorf("inFlight after a double release of a = %v, want %v", m.inFlight, want)
	}
	// a is the least loaded, then the loads tie and the rotation goes on to b.
	if got, want := holdHosts(t, m, 2), []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("picks = %v, want %v", got, want)
	}
}

func TestNextCooledProxyCatchesUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := threeProxies()
		holdHosts(t, m, 3)
		m.ReportFailure(m.proxies[0])

		// a keeps its one attempt in flight, and the picks skip it.
		if got, want := holdHosts(t, m, 4), []string{"b", "c", "b", "c"}; !slices.Equal(got, want) {
			t.Errorf("picks during the cooldown = %v, want %v", got, want)
		}
		time.Sleep(healthCooldownInitial)
		if got, want := holdHosts(t, m, 3), []string{"a", "a", "b"}; !slices.Equal(got, want) {
			t.Errorf("picks after the cooldown = %v, want %v: a takes the picks until the loads are equal", got, want)
		}
		if want := []int{3, 4, 3}; !slices.Equal(m.inFlight, want) {
			t.Errorf("inFlight = %v, want %v", m.inFlight, want)
		}
	})
}

// mustParse parses the proxy file text and fails the test on an error.
func mustParse(t *testing.T, text string) []Proxy {
	t.Helper()
	proxies, _, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return proxies
}

func TestNewManager(t *testing.T) {
	proxies := mustParse(t, "1.2.3.4:8080:user1:pass1\n5.6.7.8:9090:user2:pass2\n")
	m := NewManager(proxies)
	if !slices.Equal(m.proxies, proxies) {
		t.Errorf("proxies = %+v, want %+v", m.proxies, proxies)
	}
	if want := []int{0, 0}; !slices.Equal(m.inFlight, want) {
		t.Errorf("inFlight = %v, want %v", m.inFlight, want)
	}
}

func TestNewManagerNoProxiesRunsDirect(t *testing.T) {
	for _, proxies := range [][]Proxy{nil, {}} {
		m := NewManager(proxies)
		if p, release, err := m.Next(t.Context()); err != nil || !p.IsZero() || release == nil {
			t.Errorf("Next = %+v, release set %v, %v; want a zero Proxy, a release and no error", p, release != nil, err)
		}
	}
}

func TestNextRoundRobin(t *testing.T) {
	m := newManager(mustParse(t, "a:1:u1:p1\nb:2:u2:p2\nc:3:u3:p3\n"))

	// Should cycle a, b, c, a, b, c...
	expected := []string{"a", "b", "c", "a", "b", "c"}
	for i, want := range expected {
		got := mustNext(t, m)
		if got.Host != want {
			t.Errorf("Next() call %d: host = %q, want %q", i, got.Host, want)
		}
	}
}

func TestParse(t *testing.T) {
	const (
		a1 = "a:1:u1:p1"
		b2 = "b:2:u2:p2"
	)
	pa1 := Proxy{Scheme: "http", Host: "a", Port: "1", Username: "u1", Password: "p1"}
	pb2 := Proxy{Scheme: "http", Host: "b", Port: "2", Username: "u2", Password: "p2"}
	tests := []struct {
		name         string
		text         string
		want         []Proxy
		wantRepeated int
	}{
		{name: "empty text", text: ""},
		{name: "comments only", text: "# one\n\n  \n# two\n"},
		{name: "comments and blank lines", text: "# comment\n\n" + a1 + "\n  \n# another\n" + b2 + "\n", want: []Proxy{pa1, pb2}},
		{name: "no newline at the end", text: a1 + "\n" + b2, want: []Proxy{pa1, pb2}},
		{name: "CRLF and spaces", text: "  " + a1 + " \r\n\t" + b2 + "\r\n", want: []Proxy{pa1, pb2}},
		{name: "exact repeat", text: a1 + "\n" + b2 + "\n" + a1 + "\n", want: []Proxy{pa1, pb2}, wantRepeated: 1},
		{name: "http scheme and no scheme", text: "http://" + a1 + "\n" + a1 + "\n", want: []Proxy{pa1}, wantRepeated: 1},
		{
			name:         "socks5h and socks5",
			text:         "socks5h://a:1:u:p\nsocks5://a:1:u:p\n",
			want:         []Proxy{{Scheme: "socks5", Host: "a", Port: "1", Username: "u", Password: "p"}},
			wantRepeated: 1,
		},
		{
			name: "one host:port in http and socks5 is two proxies",
			text: a1 + "\nsocks5://a:1:u1:p1\n",
			want: []Proxy{pa1, {Scheme: "socks5", Host: "a", Port: "1", Username: "u1", Password: "p1"}},
		},
		{
			name:         "the last credentials of a host:port win in the place of the first line",
			text:         "a:1:x:y\n" + b2 + "\na:1\n" + a1 + "\n",
			want:         []Proxy{pa1, pb2},
			wantRepeated: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, repeated, err := Parse(tt.text)
			if err != nil || !slices.Equal(got, tt.want) || repeated != tt.wantRepeated {
				t.Errorf("Parse = %+v, %d, %v; want %+v, %d, nil", got, repeated, err, tt.want, tt.wantRepeated)
			}
		})
	}
}

func TestProxyHostPortZeroValue(t *testing.T) {
	p := Proxy{}
	if got := p.HostPort(); got != "" {
		t.Errorf("HostPort() = %q, want empty string", got)
	}
	if got := p.Line(); got != "" {
		t.Errorf("Line() = %q, want empty string", got)
	}
}

func TestMixedSchemeProxies(t *testing.T) {
	m := newManager(mustParse(t, "1.2.3.4:8080:u:p\nsocks5://5.6.7.8:1080:u:p\nsocks5h://9.10.11.12:1080:u:p\n"))
	if len(m.proxies) != 3 {
		t.Fatalf("proxies = %+v, want 3", m.proxies)
	}

	p1 := mustNext(t, m)
	if p1.scheme() != "http" {
		t.Errorf("p1 scheme = %q, want %q", p1.scheme(), "http")
	}

	p2 := mustNext(t, m)
	if p2.Scheme != "socks5" {
		t.Errorf("p2 scheme = %q, want %q", p2.Scheme, "socks5")
	}

	p3 := mustNext(t, m)
	if p3.Scheme != "socks5" || p3.Host != "9.10.11.12" {
		t.Errorf("p3 = %+v, want Scheme socks5 and Host 9.10.11.12", p3)
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		line         string
		wantScheme   string
		wantHostPort string
	}{
		{"1.2.3.4:8080", "http", "1.2.3.4:8080"},
		{"1.2.3.4:8080:user:pass", "http", "1.2.3.4:8080"},
		{"http://1.2.3.4:8080:user:pass", "http", "1.2.3.4:8080"},
		{"socks5://1.2.3.4:1080:user:pass", "socks5", "socks5://1.2.3.4:1080"},
		{"socks5://10.0.0.1:1080", "socks5", "socks5://10.0.0.1:1080"},
		{"socks5h://h:1080:u:p", "socks5", "socks5://h:1080"},
		{"proxy-1.example_net:65535", "http", "proxy-1.example_net:65535"},
		{"1.2.3.4:8080:user:", "http", "1.2.3.4:8080"},
		// The text before "://" holds ':', so it is part of the password and not a scheme.
		{"1.2.3.4:8080:user:pa://ss", "http", "1.2.3.4:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			p, err := ParseLine(tt.line)
			if err != nil {
				t.Fatalf("ParseLine: %v", err)
			}
			if p.Scheme != tt.wantScheme {
				t.Errorf("Scheme = %q, want %q", p.Scheme, tt.wantScheme)
			}
			assertProxyForms(t, p, tt.wantHostPort)
			assertLineRoundTrip(t, p)
			if tt.wantScheme != "http" {
				return
			}
			// A Proxy built with no Scheme, as a literal is, reads as http.
			p.Scheme = ""
			if got := p.scheme(); got != "http" {
				t.Errorf("scheme() with no Scheme = %q, want %q", got, "http")
			}
			assertProxyForms(t, p, tt.wantHostPort)
		})
	}
}

// assertProxyForms checks the HostPort form of p.
func assertProxyForms(t *testing.T, p Proxy, wantHostPort string) {
	t.Helper()
	if got := p.HostPort(); got != wantHostPort {
		t.Errorf("HostPort() = %q, want %q", got, wantHostPort)
	}
}

// assertLineRoundTrip checks that the line of p parses back to p, and that its
// HostPort parses back to p with no credentials.
func assertLineRoundTrip(t *testing.T, p Proxy) {
	t.Helper()
	if got, n, err := Parse(p.Line()); err != nil || n != 0 || len(got) != 1 || got[0] != p {
		t.Errorf("Parse(%q) = %+v, %d, %v; want [%+v]", p.Line(), got, n, err, p)
	}
	bare := p
	bare.Username, bare.Password = "", ""
	if got, err := ParseLine(p.HostPort()); err != nil || got != bare {
		t.Errorf("ParseLine(%q) = %+v, %v; want %+v", p.HostPort(), got, err, bare)
	}
	if got := bare.Line(); got != p.HostPort() {
		t.Errorf("Line() with no credentials = %q, want %q", got, p.HostPort())
	}
}

func TestLine(t *testing.T) {
	tests := []struct {
		p    Proxy
		want string
	}{
		{Proxy{Host: "a", Port: "1"}, "a:1"},
		{Proxy{Scheme: "http", Host: "a", Port: "1", Username: "u", Password: "p"}, "a:1:u:p"},
		{Proxy{Scheme: "socks5", Host: "a", Port: "1", Username: "u", Password: ""}, "socks5://a:1:u:"},
	}
	for _, tt := range tests {
		if got := tt.p.Line(); got != tt.want {
			t.Errorf("Line(%+v) = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestParseLineRefusesBadLine(t *testing.T) {
	const (
		user       = "secretuser"
		pass       = "secretpass"
		schemeErr  = "unsupported scheme: use http, socks5 or socks5h"
		hostErr    = "host must be an IPv4 address or a host name"
		portErr    = "port must be a number from 1 to 65535"
		userErr    = "empty user name"
		creds      = ":" + user + ":" + pass
		goodTarget = "1.2.3.4:8080"
	)
	tests := []struct {
		name    string
		line    string
		wantErr string
	}{
		{name: "https scheme", line: "https://9.10.11.12:443" + creds, wantErr: schemeErr},
		{name: "socks4 scheme", line: "socks4://1.2.3.4:1080", wantErr: schemeErr},
		{name: "ftp scheme", line: "ftp://1.2.3.4:21", wantErr: schemeErr},
		{name: "scheme prefix with @", line: user + "@" + pass + "://" + goodTarget, wantErr: schemeErr},
		{name: "scheme prefix with colon", line: user + ":" + pass + "://" + goodTarget, wantErr: portErr},
		{name: "three parts", line: goodTarget + ":" + user, wantErr: "expected host:port or host:port:user:pass, got 3 parts"},
		{name: "empty host", line: ":8080" + creds, wantErr: hostErr},
		{name: "host with space", line: "bad host:8080" + creds, wantErr: hostErr},
		{name: "host with slash", line: "bad/host:8080" + creds, wantErr: hostErr},
		{name: "host with @", line: "bad@host:8080" + creds, wantErr: hostErr},
		{name: "host in brackets", line: "[1.2.3.4]:8080" + creds, wantErr: hostErr},
		{name: "empty port", line: "1.2.3.4:" + creds, wantErr: portErr},
		{name: "port with letter", line: "1.2.3.4:80a" + creds, wantErr: portErr},
		{name: "negative port", line: "1.2.3.4:-1" + creds, wantErr: portErr},
		{name: "port with plus", line: "1.2.3.4:+80" + creds, wantErr: portErr},
		{name: "port 0", line: "1.2.3.4:0" + creds, wantErr: portErr},
		{name: "port 65536", line: "1.2.3.4:65536" + creds, wantErr: portErr},
		{name: "empty user", line: goodTarget + "::" + pass, wantErr: userErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseLine(tt.line)
			if err == nil {
				t.Fatalf("ParseLine = %+v, want error %q", p, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("error = %q, want %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), user) || strings.Contains(err.Error(), pass) {
				t.Errorf("error = %q, want no user and no password", err)
			}
		})
	}
}

func TestRandomStartIndex(t *testing.T) {
	proxies := mustParse(t, "a:1:u:p\nb:2:u:p\nc:3:u:p\nd:4:u:p\ne:5:u:p\n")

	// Create many managers and check that at least two different start indices
	// are observed. With 5 proxies and 50 trials, the probability of always
	// hitting index 0 is (1/5)^50 ≈ 0, so this is not flaky.
	seen := make(map[string]bool)
	for range 50 {
		first := mustNext(t, NewManager(proxies))
		seen[first.Host] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected multiple different starting proxies, only saw %v", seen)
	}
}

func TestParseBadLine(t *testing.T) {
	const bad = "host:port:extra"
	proxies, repeated, err := Parse("a:1\n# comment\n" + bad + "\nb:2\n")
	lineErr, ok := errors.AsType[*LineError](err)
	if !ok || lineErr.Line != 3 || proxies != nil || repeated != 0 {
		t.Fatalf("Parse = %v, %d, %v; want a *LineError of line 3", proxies, repeated, err)
	}
	want := "line 3: expected host:port or host:port:user:pass, got 3 parts"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if lineErr.Err == nil || !errors.Is(err, lineErr.Err) {
		t.Errorf("error = %v, want one that matches its cause %v", err, lineErr.Err)
	}
	if strings.Contains(err.Error(), bad) {
		t.Errorf("error = %q, want no line text", err.Error())
	}
}

func TestNextSkipsUnhealthyProxy(t *testing.T) {
	m := newManager(mustParse(t, "a:1:u:p\nb:2:u:p\nc:3:u:p\n"))

	// Mark proxy "b" as unhealthy.
	m.ReportFailure(Proxy{Host: "b", Port: "2"})

	// Should get a, c, a, c (skipping b each time).
	expected := []string{"a", "c", "a", "c"}
	for i, want := range expected {
		got := mustNext(t, m)
		if got.Host != want {
			t.Errorf("Next() call %d: host = %q, want %q", i, got.Host, want)
		}
	}
}

func TestNextWaitsForEarliestCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := Proxy{Host: "a", Port: "1"}, Proxy{Host: "b", Port: "2"}
		m := newManager([]Proxy{a, b})
		m.ReportFailure(b)
		time.Sleep(time.Minute)
		m.ReportFailure(a)

		start := time.Now()
		got := mustNext(t, m)
		if got.Host != "b" {
			t.Errorf("Next() host = %q, want %q", got.Host, "b")
		}
		if elapsed, want := time.Since(start), healthCooldownInitial-time.Minute; elapsed != want {
			t.Errorf("Next() waited %v, want %v", elapsed, want)
		}
	})
}

func TestNextAfterWaitKeepsRotation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxies := []Proxy{{Host: "a", Port: "1"}, {Host: "b", Port: "2"}, {Host: "c", Port: "3"}}
		m := newManager(proxies)
		for _, p := range proxies {
			m.ReportFailure(p)
		}

		// The first call waits for the cooldowns to end, then all three are healthy.
		start := time.Now()
		for i, want := range []string{"a", "b", "c"} {
			if got := mustNext(t, m); got.Host != want {
				t.Errorf("Next() call %d: host = %q, want %q", i, got.Host, want)
			}
		}
		if elapsed := time.Since(start); elapsed != healthCooldownInitial {
			t.Errorf("Next() calls waited %v, want %v", elapsed, healthCooldownInitial)
		}
	})
}

func TestNextCanceledDuringWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newManager([]Proxy{{Host: "a", Port: "1"}})
		m.ReportFailure(m.proxies[0])
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(time.Minute, cancel)

		start := time.Now()
		got, release, err := m.Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Next() error = %v, want %v", err, context.Canceled)
		}
		if !got.IsZero() || release != nil {
			t.Errorf("Next() = %+v, release set %v; want a zero Proxy and a nil release", got, release != nil)
		}
		if elapsed := time.Since(start); elapsed != time.Minute {
			t.Errorf("Next() returned after %v, want %v", elapsed, time.Minute)
		}
	})
}

func TestNextNoProxies(t *testing.T) {
	// An empty pool returns at once, also when ctx has ended.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, release, err := (&Manager{}).Next(ctx)
	if err != nil {
		t.Errorf("Next() error = %v, want nil", err)
	}
	if !got.IsZero() || release == nil {
		t.Fatalf("Next() = %+v, release set %v; want a zero Proxy and a release", got, release != nil)
	}
	release()
}

func TestReportFailureExponentialCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &Manager{}
		p := Proxy{Host: "a", Port: "1"}

		// Each report comes at the end of the cooldown before it, so each one counts.
		for i, want := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
			cooldown, failures, started := m.ReportFailure(p)
			if cooldown != want || failures != i+1 || !started {
				t.Errorf("report %d = %v, %d, %v; want %v, %d, true", i+1, cooldown, failures, started, want, i+1)
			}
			time.Sleep(cooldown)
		}
	})
}

func TestReportFailureDuringCooldown(t *testing.T) {
	tests := []struct {
		name         string
		elapsed      time.Duration // the time from the first report to the second
		wantCooldown time.Duration
		wantFailures int
		wantStarted  bool
		wantWait     time.Duration // the wait of Next after the second report
	}{
		{name: "at the cooldown start", elapsed: 0, wantWait: healthCooldownInitial},
		{name: "just before the cooldown end", elapsed: healthCooldownInitial - time.Nanosecond, wantWait: time.Nanosecond},
		{
			name:         "at the cooldown end",
			elapsed:      healthCooldownInitial,
			wantCooldown: 10 * time.Minute,
			wantFailures: 2,
			wantStarted:  true,
			wantWait:     10 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := Proxy{Host: "a", Port: "1"}
				m := newManager([]Proxy{p})
				m.ReportFailure(p)
				time.Sleep(tt.elapsed)

				cooldown, failures, started := m.ReportFailure(p)
				if cooldown != tt.wantCooldown || failures != tt.wantFailures || started != tt.wantStarted {
					t.Errorf("ReportFailure = %v, %d, %v; want %v, %d, %v",
						cooldown, failures, started, tt.wantCooldown, tt.wantFailures, tt.wantStarted)
				}
				start := time.Now()
				mustNext(t, m)
				if waited := time.Since(start); waited != tt.wantWait {
					t.Errorf("Next() waited %v, want %v", waited, tt.wantWait)
				}
			})
		})
	}
}

func TestReportSuccess(t *testing.T) {
	// Three failures put "a" in a cooldown of 4*healthCooldownInitial.
	const cooldown = 4 * healthCooldownInitial
	tests := []struct {
		name         string
		elapsed      time.Duration
		wantHost     string
		wantFailures int
		wantCooldown time.Duration
	}{
		{
			name:         "during the cooldown",
			elapsed:      cooldown - time.Nanosecond,
			wantHost:     "b",
			wantFailures: 4,
			wantCooldown: healthCooldownMax,
		},
		{
			name:         "after the cooldown",
			elapsed:      cooldown,
			wantHost:     "a",
			wantFailures: 1,
			wantCooldown: healthCooldownInitial,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := Proxy{Host: "a", Port: "1"}
				m := newManager([]Proxy{a, {Host: "b", Port: "2"}})
				// A report during a running cooldown does not count, so each failure waits out the one before.
				m.ReportFailure(a)
				time.Sleep(healthCooldownInitial)
				m.ReportFailure(a)
				time.Sleep(2 * healthCooldownInitial)
				m.ReportFailure(a)
				time.Sleep(tt.elapsed)

				m.ReportSuccess(a)
				if got := mustNext(t, m); got.Host != tt.wantHost {
					t.Errorf("Next() host = %q, want %q", got.Host, tt.wantHost)
				}
				// The next report lands at the cooldown end, so it counts in both cases.
				time.Sleep(cooldown - tt.elapsed)
				gotCooldown, gotFailures, started := m.ReportFailure(a)
				if gotFailures != tt.wantFailures || !started {
					t.Errorf("failures = %d, started %v; want %d, true", gotFailures, started, tt.wantFailures)
				}
				if gotCooldown != tt.wantCooldown {
					t.Errorf("cooldown = %v, want %v", gotCooldown, tt.wantCooldown)
				}
			})
		})
	}
}

func TestNextConcurrent(t *testing.T) {
	m := newManager(mustParse(t, "a:1:u:p\nb:2:u:p\nc:3:u:p\n"))

	const goroutines = 8
	const callsPerGoroutine = 1000

	synctest.Test(t, func(t *testing.T) {
		var wg sync.WaitGroup
		for range goroutines {
			wg.Go(func() {
				for range callsPerGoroutine {
					p, _, err := m.Next(t.Context())
					if err != nil || p.IsZero() {
						t.Errorf("Next() = %+v, %v; want a proxy", p, err)
						return
					}
				}
			})
		}
		wg.Wait()
	})

	// No call released its proxy, so each call counts once and the loads stay even.
	if sum, want := m.inFlight[0]+m.inFlight[1]+m.inFlight[2], goroutines*callsPerGoroutine; sum != want {
		t.Errorf("inFlight = %v, sum %d; want the sum %d", m.inFlight, sum, want)
	}
	if spread := slices.Max(m.inFlight) - slices.Min(m.inFlight); spread > 1 {
		t.Errorf("inFlight = %v, want loads that differ by at most 1", m.inFlight)
	}
}
