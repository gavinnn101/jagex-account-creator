package email

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"jagex_account_creator/internal/logtest"
)

func TestIMAPProvider_Address(t *testing.T) {
	p := &IMAPProvider{Domain: "example.com"}
	if got, want := p.Address("abc123"), "abc123@example.com"; got != want {
		t.Errorf("Address = %q, want %q", got, want)
	}
}

func TestIMAPProvider_PollInterval(t *testing.T) {
	tests := []struct {
		set, want time.Duration
	}{
		{0, 2 * time.Second},
		{10 * time.Millisecond, 10 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := (&IMAPProvider{PollInterval: tt.set}).pollInterval(); got != tt.want {
			t.Errorf("pollInterval with PollInterval %v = %v, want %v", tt.set, got, tt.want)
		}
	}
}

// serveFunc writes the server's replies to one client line.
type serveFunc func(w io.Writer, line string)

const (
	idleCaps = "IMAP4rev1 IDLE"
	// idleGreeting is the greeting of a server that supports IDLE.
	idleGreeting = "* OK [CAPABILITY " + idleCaps + "] ready"
	// plainGreeting is the greeting of a server that lacks IDLE, so Idle falls back to NOOP polling.
	plainGreeting = "* OK [CAPABILITY IMAP4rev1] ready"
)

// errDial is the error of a failed fake dial.
var errDial = errors.New("dial refused")

// discardLogger returns a logger that drops every record.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeDialer is a dialFn that gives each call its own pipe to a fake server.
// The server writes greeting first, unless it is empty, and passes each client line to the session of its connection.
type fakeDialer struct {
	greeting string
	session  func() serveFunc // returns the line handler of one new connection
	fails    int              // the first fails calls return errDial

	mu       sync.Mutex
	dials    int
	commands []string        // the command names that the servers read, in read order
	closed   []chan struct{} // each one closes when the server of that connection reads the end of the stream
}

// dialerFor returns a fakeDialer whose connections all use serve. serve keeps no state that two connections can share.
func dialerFor(greeting string, serve serveFunc) *fakeDialer {
	return &fakeDialer{greeting: greeting, session: func() serveFunc { return serve }}
}

func (d *fakeDialer) dial(context.Context, string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials++
	if d.dials <= d.fails {
		return nil, errDial
	}
	closed := make(chan struct{})
	d.closed = append(d.closed, closed)
	clientSide, serverSide := net.Pipe()
	go d.run(serverSide, d.session(), closed)
	return clientSide, nil
}

func (d *fakeDialer) run(conn net.Conn, serve serveFunc, closed chan<- struct{}) {
	defer close(closed)
	defer func() { _ = conn.Close() }()
	if d.greeting != "" {
		_, _ = io.WriteString(conn, d.greeting+"\r\n")
	}
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		_, name := parseCommand(line)
		if name == "" {
			name = line
		}
		d.mu.Lock()
		d.commands = append(d.commands, name)
		d.mu.Unlock()
		serve(conn, line)
	}
}

func (d *fakeDialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

func (d *fakeDialer) commandList() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.commands)
}

// assertClosed fails t when a server still has an open connection. Call it inside a synctest bubble.
func (d *fakeDialer) assertClosed(t *testing.T) {
	t.Helper()
	synctest.Wait()
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, ch := range d.closed {
		select {
		case <-ch:
		default:
			t.Errorf("connection %d is still open", i+1)
		}
	}
}

// parseCommand returns the tag and the command name of a client line.
func parseCommand(line string) (tag, name string) {
	tag, command, _ := strings.Cut(line, " ")
	name, _, _ = strings.Cut(command, " ")
	return tag, name
}

// okReply answers line with a tagged OK. The OK of LOGIN lists caps, because a login drops the cached capabilities.
func okReply(w io.Writer, line, caps string) {
	tag, name := parseCommand(line)
	if name == "LOGIN" {
		_, _ = fmt.Fprintf(w, "%s OK [CAPABILITY %s] logged in\r\n", tag, caps)
		return
	}
	_, _ = fmt.Fprintf(w, "%s OK done\r\n", tag)
}

// okIdle answers every command with OK, as a server that supports IDLE.
func okIdle(w io.Writer, line string) { okReply(w, line, idleCaps) }

// refuseLogin answers LOGIN with NO and every other command with OK.
func refuseLogin(w io.Writer, line string) {
	if tag, name := parseCommand(line); name == "LOGIN" {
		_, _ = fmt.Fprintf(w, "%s NO Invalid credentials\r\n", tag)
		return
	}
	okIdle(w, line)
}

// stallOn returns a server that never answers the command name and answers every other command with OK.
func stallOn(name string) serveFunc {
	return func(w io.Writer, line string) {
		if _, got := parseCommand(line); got != name {
			okIdle(w, line)
		}
	}
}

// connCase is one connection outcome. A case with no wantCommands does not check the commands.
type connCase struct {
	name         string
	greeting     string
	serve        serveFunc
	fails        int
	wantErr      string
	wantLogin    bool
	wantAfter    time.Duration
	wantCommands []string
}

// runConnCase runs call in a bubble against the server of tt. It checks the error, the
// duration, the commands, and that call closed every connection.
func runConnCase(t *testing.T, tt connCase, call func(ctx context.Context, p *IMAPProvider) error) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		d := dialerFor(tt.greeting, tt.serve)
		d.fails = tt.fails
		p := &IMAPProvider{Logger: discardLogger(), dialFn: d.dial}
		start := time.Now()
		err := call(t.Context(), p)
		elapsed := time.Since(start)
		if gotErr := fmt.Sprint(err); (tt.wantErr == "" && err != nil) || (tt.wantErr != "" && gotErr != tt.wantErr) {
			t.Errorf("error = %v, want %q", err, tt.wantErr)
		}
		if errors.Is(err, ErrEmailLogin) != tt.wantLogin {
			t.Errorf("errors.Is(%v, ErrEmailLogin) = %v, want %v", err, !tt.wantLogin, tt.wantLogin)
		}
		if elapsed != tt.wantAfter {
			t.Errorf("returned after %v, want exactly %v", elapsed, tt.wantAfter)
		}
		d.assertClosed(t)
		if got := d.commandList(); tt.wantCommands != nil && !slices.Equal(got, tt.wantCommands) {
			t.Errorf("server read %v, want %v", got, tt.wantCommands)
		}
	})
}

func TestIMAPProvider_Connect(t *testing.T) {
	t.Run("good login", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var logs bytes.Buffer
			d := dialerFor(idleGreeting, okIdle)
			p := &IMAPProvider{Logger: logtest.New(&logs), dialFn: d.dial}
			c, closeConn, err := p.connect(t.Context())
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			if c.Timeout != imapCommandTimeout {
				t.Errorf("command timeout = %v, want %v", c.Timeout, imapCommandTimeout)
			}
			// A closed socket makes the reader fail, and the provider's logger gets its error.
			_ = c.Terminate()
			<-c.LoggedOut()
			closeConn()
			if got, want := logs.String(), "level=DEBUG msg=\"error reading response: io: read/write on closed pipe\"\n"; got != want {
				t.Errorf("log = %q, want %q", got, want)
			}
			d.assertClosed(t)
		})
	})

	const deadline = 5 * time.Second
	stalled := "IMAP connection: context deadline exceeded"
	tests := []connCase{
		{name: "LOGIN NO", greeting: idleGreeting, serve: refuseLogin,
			wantErr: "IMAP login failed: Invalid credentials", wantLogin: true, wantCommands: []string{"LOGIN"}},
		{name: "dial error", fails: 1, wantErr: "connecting to IMAP: dial refused"},
		{name: "bad greeting", greeting: "* BAD not ready", wantErr: "initializing IMAP client: invalid greeting received from server: BAD"},
		{name: "stall at CAPABILITY", greeting: "* OK ready", serve: stallOn("CAPABILITY"), wantErr: stalled, wantAfter: deadline},
		{name: "stall at LOGIN", greeting: idleGreeting, serve: stallOn("LOGIN"), wantErr: stalled, wantAfter: deadline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runConnCase(t, tt, func(ctx context.Context, p *IMAPProvider) error {
				ctx, cancel := context.WithTimeout(ctx, deadline)
				defer cancel()
				_, _, err := p.connect(ctx)
				return err
			})
		})
	}
}

func TestIMAPProvider_CheckLogin(t *testing.T) {
	tests := []connCase{
		{name: "good login", greeting: idleGreeting, serve: okIdle, wantCommands: []string{"LOGIN", "LOGOUT"}},
		{name: "LOGIN NO", greeting: idleGreeting, serve: refuseLogin, wantErr: "IMAP login failed: Invalid credentials", wantLogin: true},
		{name: "dial error", fails: 1, wantErr: "connecting to IMAP: dial refused"},
		{name: "stalled server", serve: okIdle, wantErr: "IMAP connection: context deadline exceeded", wantAfter: imapCommandTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runConnCase(t, tt, func(ctx context.Context, p *IMAPProvider) error { return p.CheckLogin(ctx) })
		})
	}
}

func TestIMAPProvider_ConnectClose(t *testing.T) {
	tests := []struct {
		name string
		wait time.Duration // the time between the login and the close
	}{
		{"live connection", 0},
		// The read deadline of LOGIN ends the reader, so no reply to LOGOUT is read.
		{"dead reader", imapCommandTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := dialerFor(idleGreeting, okIdle)
				p := &IMAPProvider{Logger: discardLogger(), dialFn: d.dial}
				ctx, cancel := context.WithTimeout(t.Context(), 2*imapCommandTimeout)
				defer cancel()
				c, closeConn, err := p.connect(ctx)
				if err != nil {
					t.Fatalf("connect: %v", err)
				}
				if tt.wait > 0 {
					time.Sleep(tt.wait)
					<-c.LoggedOut()
				}
				closeConn()
				d.assertClosed(t)
				if got, want := d.commandList(), []string{"LOGIN", "LOGOUT"}; !slices.Equal(got, want) {
					t.Errorf("server read %v, want %v", got, want)
				}
			})
		})
	}
}

// pipeClient returns a logged-in client over an in-process pipe. Its server greets with caps
// and passes each client line to serve, which writes the replies.
// The greeting caches the capabilities, so Idle sends no CAPABILITY command.
func pipeClient(t *testing.T, caps string, serve serveFunc) *client.Client {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	go func() {
		defer func() { _ = serverSide.Close() }()
		_, _ = fmt.Fprintf(serverSide, "* PREAUTH [CAPABILITY %s] ready\r\n", caps)
		scanner := bufio.NewScanner(serverSide)
		for scanner.Scan() {
			serve(serverSide, scanner.Text())
		}
	}()

	c, err := client.New(clientSide)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	c.ErrorLog = slog.NewLogLogger(slog.DiscardHandler, slog.LevelDebug)
	t.Cleanup(func() { _ = c.Terminate() })
	return c
}

// mailSession serves one message in INBOX. The server lacks IDLE, so Idle falls back to NOOP polling.
func mailSession(message string) serveFunc {
	return func(w io.Writer, line string) {
		switch _, name := parseCommand(line); name {
		case "SEARCH":
			_, _ = io.WriteString(w, "* SEARCH 1\r\n")
		case "FETCH":
			_, _ = fmt.Fprintf(w, "* 1 FETCH (BODY[] {%d}\r\n%s)\r\n", len(message), message)
		}
		okReply(w, line, "IMAP4rev1")
	}
}

// verificationMessage returns a mail whose code is readable only after the body is decoded.
func verificationMessage() string {
	return "Content-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(htmlBody))
}

// scriptedIDLESession serves an empty INBOX on a server that supports IDLE.
// The server answers DONE only when answerDone is set, and sends EXISTS during IDLE when existsInIdle is set.
func scriptedIDLESession(answerDone, existsInIdle bool) serveFunc {
	var idleTag string
	return func(w io.Writer, line string) {
		if line == "DONE" {
			if answerDone {
				_, _ = fmt.Fprintf(w, "%s OK idle done\r\n", idleTag)
			}
			return
		}
		tag, name := parseCommand(line)
		if name == "IDLE" {
			idleTag = tag
			_, _ = io.WriteString(w, "+ idling\r\n")
			if existsInIdle {
				_, _ = io.WriteString(w, "* 2 EXISTS\r\n")
			}
			return
		}
		okIdle(w, line)
	}
}

// lateMailSession serves a server that lacks IDLE. The mail arrives with the first NOOP,
// the first poll: that NOOP reports EXISTS, and later SEARCH commands find it.
func lateMailSession(message string) serveFunc {
	arrived := false
	return func(w io.Writer, line string) {
		switch _, name := parseCommand(line); {
		case name == "NOOP" && !arrived:
			arrived = true
			_, _ = io.WriteString(w, "* 1 EXISTS\r\n")
		case name == "SEARCH" && arrived:
			_, _ = io.WriteString(w, "* SEARCH 1\r\n")
		case name == "SEARCH":
			_, _ = io.WriteString(w, "* SEARCH\r\n")
		case name == "FETCH":
			_, _ = fmt.Fprintf(w, "* 1 FETCH (BODY[] {%d}\r\n%s)\r\n", len(message), message)
		}
		okReply(w, line, "IMAP4rev1")
	}
}

func TestIMAPProvider_WaitIDLE_ReturnsCodeAfterUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := dialerFor(plainGreeting, lateMailSession(verificationMessage()))
		p := &IMAPProvider{PollInterval: 10 * time.Millisecond, Logger: discardLogger(), dialFn: d.dial}

		// The deadline is far below the 2s default interval, so a lost PollInterval fails the wait.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		code, err := p.waitIDLE(ctx, "test@example.com", time.Time{}, p.Logger)
		if err != nil {
			t.Fatalf("waitIDLE: %v", err)
		}
		if code != "ABC123" {
			t.Errorf("code = %q, want %q", code, "ABC123")
		}
		d.assertClosed(t)
	})
}

func TestIMAPProvider_WaitIDLE_ReturnsCodeOnFirstCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := dialerFor(plainGreeting, mailSession(verificationMessage()))
		p := &IMAPProvider{PollInterval: time.Hour, Logger: discardLogger(), dialFn: d.dial}

		code, err := p.waitIDLE(t.Context(), "test@example.com", time.Time{}, p.Logger)
		if err != nil {
			t.Fatalf("waitIDLE: %v", err)
		}
		if code != "ABC123" {
			t.Errorf("code = %q, want %q", code, "ABC123")
		}
		d.assertClosed(t)
		if got, want := d.commandList(), []string{"LOGIN", "SELECT", "SEARCH", "FETCH", "LOGOUT"}; !slices.Equal(got, want) {
			t.Errorf("server read %v, want %v", got, want)
		}
	})
}

func TestIMAPProvider_WaitIDLE_ContextEndsWhileServerIgnoresDONE(t *testing.T) {
	// net.Pipe is channel-based, so the bubble's fake clock makes the duration exact.
	synctest.Test(t, func(t *testing.T) {
		d := dialerFor(idleGreeting, scriptedIDLESession(false, false))
		p := &IMAPProvider{PollInterval: time.Hour, Logger: discardLogger(), dialFn: d.dial}

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := p.waitIDLE(ctx, "test@example.com", time.Time{}, p.Logger)
		// The context also closes the connection, so the IDLE command can fail first.
		if !errors.Is(err, context.DeadlineExceeded) && fmt.Sprint(err) != "IDLE: "+imapErrClosedText {
			t.Fatalf("waitIDLE error = %v, want a deadline or a closed connection", err)
		}
		if elapsed := time.Since(start); elapsed != 100*time.Millisecond {
			t.Errorf("waitIDLE returned after %v, want exactly the 100ms context", elapsed)
		}
		d.assertClosed(t)
	})
}

func TestIMAPProvider_WaitIDLE_DrainsSelectUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		idle := scriptedIDLESession(true, false)
		d := dialerFor(idleGreeting, func(w io.Writer, line string) {
			if _, name := parseCommand(line); name == "SELECT" {
				_, _ = io.WriteString(w, "* 3 EXISTS\r\n* 1 RECENT\r\n")
			}
			idle(w, line)
		})
		p := &IMAPProvider{PollInterval: time.Hour, Logger: discardLogger(), dialFn: d.dial}

		// The context ends before the first IDLE restart at half of the command timeout.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if _, err := p.waitIDLE(ctx, "test@example.com", time.Time{}, p.Logger); err == nil {
			t.Fatal("waitIDLE returned no error after its context ended")
		}
		d.assertClosed(t)
		commands := d.commandList()
		for _, name := range []string{"SEARCH", "IDLE"} {
			n := 0
			for _, got := range commands {
				if got == name {
					n++
				}
			}
			if n != 1 {
				t.Errorf("server read %d %s commands (%v), want 1", n, name, commands)
			}
		}
	})
}

// imapErrClosedText is the text of the error go-imap returns when the connection closes
// during a command. go-imap does not export that error.
const imapErrClosedText = "imap: connection closed"

func TestIMAPProvider_WaitForVerificationCode_Errors(t *testing.T) {
	const (
		addr     = "test@example.com"
		timeout  = 10 * time.Second
		interval = 3 * time.Second
		prefix   = "waiting for verification email to " + addr + ": "
	)
	cancelAt := func(ctx context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(2*time.Second, cancel)
		return ctx, cancel
	}
	deadlineAt := func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 2*time.Second)
	}
	noMail := scriptedIDLESession(true, false)
	tests := []struct {
		name      string
		fails     int
		serve     serveFunc
		parent    func(context.Context) (context.Context, context.CancelFunc)
		wantCode  string
		wantErr   string
		wantIs    error
		wantDials int
		wantAfter time.Duration
	}{
		{name: "own timeout, no IMAP error", serve: noMail,
			wantErr: prefix + "email verification timed out", wantIs: ErrEmailTimeout, wantDials: 1, wantAfter: timeout},
		{name: "own timeout after dial failures", fails: 100,
			wantErr: prefix + "email verification timed out (last IMAP error: connecting to IMAP: dial refused)",
			wantIs:  ErrEmailTimeout, wantDials: 4, wantAfter: timeout},
		{name: "parent cancel", serve: noMail, parent: cancelAt,
			wantErr: prefix + "context canceled", wantIs: context.Canceled, wantDials: 1, wantAfter: 2 * time.Second},
		{name: "earlier parent deadline", serve: noMail, parent: deadlineAt,
			wantErr: prefix + "context deadline exceeded", wantIs: context.DeadlineExceeded, wantDials: 1, wantAfter: 2 * time.Second},
		// The end of the context closes the socket under SEARCH, and that failure is not kept.
		{name: "failure caused by the context end", serve: stallOn("SEARCH"),
			wantErr: prefix + "email verification timed out", wantIs: ErrEmailTimeout, wantDials: 1, wantAfter: timeout},
		{name: "LOGIN NO returns at once", serve: refuseLogin,
			wantErr: prefix + "IMAP login failed: Invalid credentials", wantIs: ErrEmailLogin, wantDials: 1},
		{name: "dial failure, then the code", fails: 1, serve: mailSession(verificationMessage()),
			wantCode: "ABC123", wantDials: 2, wantAfter: interval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := dialerFor(idleGreeting, tt.serve)
				d.fails = tt.fails
				p := &IMAPProvider{PollInterval: interval, Logger: discardLogger(), dialFn: d.dial}
				ctx := t.Context()
				if tt.parent != nil {
					var cancel context.CancelFunc
					ctx, cancel = tt.parent(ctx)
					defer cancel()
				}
				start := time.Now()
				code, err := p.WaitForVerificationCode(ctx, addr, time.Time{}, timeout)
				elapsed := time.Since(start)
				if code != tt.wantCode {
					t.Errorf("code = %q, want %q", code, tt.wantCode)
				}
				if got := fmt.Sprint(err); (tt.wantErr == "" && err != nil) || (tt.wantErr != "" && got != tt.wantErr) {
					t.Errorf("error = %v, want %q", err, tt.wantErr)
				}
				if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
					t.Errorf("error = %v, want it to match %v", err, tt.wantIs)
				}
				if errors.Is(err, ErrEmailTimeout) != errors.Is(tt.wantIs, ErrEmailTimeout) {
					t.Errorf("errors.Is(%v, ErrEmailTimeout) = %v, want %v", err, !errors.Is(tt.wantIs, ErrEmailTimeout), errors.Is(tt.wantIs, ErrEmailTimeout))
				}
				if got := d.dialCount(); got != tt.wantDials {
					t.Errorf("dials = %d, want %d", got, tt.wantDials)
				}
				if elapsed != tt.wantAfter {
					t.Errorf("returned after %v, want exactly %v", elapsed, tt.wantAfter)
				}
				d.assertClosed(t)
			})
		})
	}
}

// signalWriter keeps the log lines in buf and sends on seen when a line holds mark.
// The text handler writes each record with one Write call.
type signalWriter struct {
	buf  bytes.Buffer
	mark []byte
	seen chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	_, _ = w.buf.Write(p)
	if bytes.Contains(p, w.mark) {
		select {
		case w.seen <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

// refusedPort returns a loopback port that refuses connections: its listener is closed.
func refusedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", ln.Addr())
	}
	_ = ln.Close()
	return addr.Port
}

func TestIMAPProvider_WaitRetriesAfterDialFailure(t *testing.T) {
	port := refusedPort(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	// The dial error text differs by OS, so the test takes it from its own dial.
	_, dialErr := net.Dial("tcp", addr)
	if dialErr == nil {
		t.Fatalf("dial %s: want a refused connection", addr)
	}
	logs := &signalWriter{mark: []byte(`msg="IMAP wait failed, trying again"`), seen: make(chan struct{}, 1)}
	p := &IMAPProvider{Host: "127.0.0.1", Port: port, Domain: "example.com", PollInterval: 10 * time.Millisecond, Logger: logtest.New(logs)}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.WaitForVerificationCode(ctx, "test@example.com", time.Now(), time.Minute)
		done <- err
	}()
	select {
	case <-logs.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("no failed wait was logged within 2s")
	}
	cancel()
	cause := "connecting to IMAP: " + dialErr.Error()
	want := "waiting for verification email to test@example.com: context canceled (last IMAP error: " + cause + ")"
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForVerificationCode did not return within 2s of the cancel")
	}
	if err == nil || err.Error() != want {
		t.Fatalf("WaitForVerificationCode error = %v, want %q", err, want)
	}

	wantLine := `level=DEBUG msg="IMAP wait failed, trying again" email=test@example.com error=` + fmt.Sprintf("%q", cause)
	if got, _, _ := strings.Cut(logs.buf.String(), "\n"); got != wantLine {
		t.Errorf("first log line =\n%s\nwant\n%s", got, wantLine)
	}
}

func TestIMAPProvider_IdleOnce_CommandTimeoutBoundsDONE(t *testing.T) {
	// net.Pipe is channel-based, so the bubble's fake clock makes the duration exact.
	synctest.Test(t, func(t *testing.T) {
		c := pipeClient(t, idleCaps, scriptedIDLESession(false, true))
		c.Timeout = 300 * time.Millisecond
		updates := make(chan client.Update, 4)
		c.Updates = updates
		// EXISTS reaches the updates channel only with a mailbox selected.
		if _, err := c.Select("INBOX", false); err != nil {
			t.Fatalf("Select: %v", err)
		}

		start := time.Now()
		got, err := idleOnce(t.Context(), c, updates, time.Hour)
		elapsed := time.Since(start)
		if got || fmt.Sprint(err) != "IDLE: "+imapErrClosedText {
			t.Errorf("idleOnce = %v, %v, want no update and the IDLE error of the failed DONE", got, err)
		}
		if elapsed != 300*time.Millisecond {
			t.Errorf("idleOnce returned after %v, want exactly the 300ms command timeout", elapsed)
		}
	})
}

func TestIMAPProvider_IdleOnce_RestartOutlivesCommandTimeout(t *testing.T) {
	// net.Pipe is channel-based, so the bubble's fake clock makes the duration exact.
	synctest.Test(t, func(t *testing.T) {
		c := pipeClient(t, idleCaps, scriptedIDLESession(true, false))
		c.Timeout = 500 * time.Millisecond

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		start := time.Now()
		_, err := idleOnce(ctx, c, make(chan client.Update, 2), time.Hour)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("idleOnce error = %v, want the context to end an IDLE that outlives the 500ms command timeout", err)
		}
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("idleOnce returned after %v, want exactly the 2s context", elapsed)
		}
	})
}

func TestDrainUpdates(t *testing.T) {
	ch := make(chan client.Update, 10)

	// Send some updates.
	for range 5 {
		ch <- &client.MailboxUpdate{}
	}

	drainUpdates(ch)

	if len(ch) != 0 {
		t.Errorf("expected channel to be drained, got %d items", len(ch))
	}

	// Draining an empty channel should not block.
	drainUpdates(ch)
}

func TestIMAPProvider_IdleOnce(t *testing.T) {
	const origTimeout = 7 * time.Second
	tests := []struct {
		name       string
		cancel     bool
		update     client.Update
		disconnect bool
		wantUpdate bool
		wantErr    string
	}{
		{name: "cancelled context", cancel: true, wantErr: "context canceled"},
		{name: "mailbox update", update: &client.MailboxUpdate{}, wantUpdate: true},
		{name: "other update", update: &client.ExpungeUpdate{}},
		{name: "IDLE error", disconnect: true, wantErr: "IDLE: disconnected while idling"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := pipeClient(t, "IMAP4rev1", mailSession(""))
				c.Timeout = origTimeout

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.cancel {
					cancel()
				}
				// The second update stays pending until the round drains it.
				updates := make(chan client.Update, 2)
				if tt.update != nil {
					updates <- tt.update
					updates <- tt.update
				}
				if tt.disconnect {
					_ = c.Terminate()
					<-c.LoggedOut()
				}

				// The long interval keeps the NOOP fallback silent, so only the case under test ends the round.
				got, err := idleOnce(ctx, c, updates, time.Hour)

				if tt.wantErr == "" && err != nil {
					t.Fatalf("idleOnce: %v", err)
				}
				if tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
					t.Fatalf("idleOnce error = %v, want %q", err, tt.wantErr)
				}
				if got != tt.wantUpdate {
					t.Errorf("idleOnce = %v, want %v", got, tt.wantUpdate)
				}
				if c.Timeout != origTimeout {
					t.Errorf("command timeout = %v, want it unchanged at %v", c.Timeout, origTimeout)
				}
				if len(updates) != 0 {
					t.Errorf("%d updates left pending, want them drained", len(updates))
				}
			})
		})
	}
}

func TestAddressedTo(t *testing.T) {
	to := func(addrs ...string) *imap.Message {
		env := &imap.Envelope{}
		for _, a := range addrs {
			mailbox, host, _ := strings.Cut(a, "@")
			env.To = append(env.To, &imap.Address{MailboxName: mailbox, HostName: host})
		}
		return &imap.Message{Envelope: env}
	}
	tests := []struct {
		name string
		msg  *imap.Message
		want bool
	}{
		{"no envelope matches", &imap.Message{}, true},
		{"exact address", to("other@example.com", "test@example.com"), true},
		{"address in another case", to("Test@Example.COM"), true},
		{"other address", to("other@example.com"), false},
		{"empty recipient list", to(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addressedTo(tt.msg, "test@example.com"); got != tt.want {
				t.Errorf("addressedTo = %v, want %v", got, tt.want)
			}
		})
	}
}

// failingLiteral is a message body whose read fails.
type failingLiteral struct{}

func (failingLiteral) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingLiteral) Len() int                 { return 1 }

func TestCodeFromMessage(t *testing.T) {
	section := &imap.BodySectionName{}
	withBody := func(body imap.Literal) *imap.Message {
		return &imap.Message{Body: map[*imap.BodySectionName]imap.Literal{section: body}}
	}
	text := func(s string) imap.Literal { return strings.NewReader(s) }
	// A multipart body with no text part fails to decode, so the raw bytes are searched.
	undecodable := "Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: image/png\r\n\r\n" + htmlBody + "\r\n--b--\r\n"

	tests := []struct {
		name   string
		msg    *imap.Message
		want   string
		wantOK bool
	}{
		{"no body section", &imap.Message{}, "", false},
		{"body read fails", withBody(failingLiteral{}), "", false},
		{"decoded body holds the code", withBody(text(verificationMessage())), "ABC123", true},
		{"undecodable body falls back to the raw bytes", withBody(text(undecodable)), "ABC123", true},
		{"unparsable header falls back to the raw bytes", withBody(text("no colon on this line\r\n" + htmlBody)), "ABC123", true},
		{"body without a code", withBody(text("Content-Type: text/plain\r\n\r\nhello")), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := codeFromMessage(tt.msg, section)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("codeFromMessage = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// mailboxMessage is one verification mail. An empty code gives a body without a code.
type mailboxMessage struct {
	to, code string
	seen     bool
}

// sharedMailbox is an INBOX that several connections read. Message i has sequence number i+1.
type sharedMailbox struct {
	mu      sync.Mutex
	msgs    []mailboxMessage
	fetched []uint32 // the sequence number of each FETCH response, in send order
}

// session returns the line handler of a new connection to mb. SEARCH honors UNSEEN and TO.
// FETCH sets \Seen only after a read-write SELECT, as a real server does for BODY[].
func (mb *sharedMailbox) session() serveFunc {
	readWrite := false
	return func(w io.Writer, line string) {
		_, name := parseCommand(line)
		mb.mu.Lock()
		defer mb.mu.Unlock()
		switch name {
		case "SELECT", "EXAMINE":
			readWrite = name == "SELECT"
			_, _ = fmt.Fprintf(w, "* %d EXISTS\r\n", len(mb.msgs))
		case "SEARCH":
			_, _ = io.WriteString(w, mb.search(line))
		case "FETCH":
			mb.fetch(w, strings.Fields(line)[2], readWrite)
		}
		okReply(w, line, "IMAP4rev1")
	}
}

// search returns the SEARCH response to the command line. Must be called with mb.mu held.
func (mb *sharedMailbox) search(line string) string {
	reply := []string{"* SEARCH"}
	for i, m := range mb.msgs {
		if m.seen && strings.Contains(line, " UNSEEN") {
			continue
		}
		if strings.Contains(line, " TO ") && !strings.Contains(line, ` TO "`+m.to+`"`) {
			continue
		}
		reply = append(reply, strconv.Itoa(i+1))
	}
	return strings.Join(reply, " ") + "\r\n"
}

// fetch writes the FETCH responses for the sequence set. Must be called with mb.mu held.
func (mb *sharedMailbox) fetch(w io.Writer, seqSet string, readWrite bool) {
	set, _ := imap.ParseSeqSet(seqSet)
	for i := range mb.msgs {
		seq := uint32(i + 1)
		if !set.Contains(seq) {
			continue
		}
		m := &mb.msgs[i]
		local, host, _ := strings.Cut(m.to, "@")
		body := "Content-Type: text/html\r\n\r\n" +
			`<span data-testid="registration-started-verification-code">` + m.code + "</span>"
		// The envelope holds only the To field. Five NIL fields come before it, and four come after it.
		envelope := fmt.Sprintf(`(%s(("" NIL %q %q))%s)`, strings.Repeat("NIL ", 5), local, host, strings.Repeat(" NIL", 4))
		_, _ = fmt.Fprintf(w, "* %d FETCH (BODY[] {%d}\r\n%s ENVELOPE %s)\r\n", seq, len(body), body, envelope)
		m.seen = m.seen || readWrite
		mb.fetched = append(mb.fetched, seq)
	}
}

// fetchedSince returns the sequence numbers that FETCH responses carried after the first n.
func (mb *sharedMailbox) fetchedSince(n int) []uint32 {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	return slices.Clone(mb.fetched[n:])
}

func TestIMAPProvider_SharedMailbox(t *testing.T) {
	const addrA, addrB = "aaaaaaaaaaaa@example.com", "bbbbbbbbbbbb@example.com"
	type worker struct{ addr, want string }
	tests := []struct {
		name    string
		msgs    []mailboxMessage
		workers []worker // in wait order
	}{
		{"two workers", []mailboxMessage{{to: addrA, code: "AAA111"}, {to: addrB, code: "BBB222"}},
			[]worker{{addrA, "AAA111"}, {addrB, "BBB222"}}},
		{"first mail has no code", []mailboxMessage{{to: addrA}, {to: addrB, code: "BBB222"}, {to: addrA, code: "AAA111"}},
			[]worker{{addrA, "AAA111"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				mb := &sharedMailbox{msgs: slices.Clone(tt.msgs)}
				d := &fakeDialer{greeting: plainGreeting, session: mb.session}
				p := &IMAPProvider{PollInterval: 10 * time.Millisecond, Logger: discardLogger(), dialFn: d.dial}
				for _, w := range tt.workers {
					before := len(mb.fetchedSince(0))
					// The deadline turns a wait that never finds the code into a failure.
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					got, err := p.waitIDLE(ctx, w.addr, time.Time{}, p.Logger)
					cancel()
					if got != w.want {
						t.Errorf("%s got %q (error %v), want %q", w.addr, got, err, w.want)
					}
					for _, seq := range mb.fetchedSince(before) {
						if to := tt.msgs[seq-1].to; to != w.addr {
							t.Errorf("%s fetched message %d to %s, want only its own mail", w.addr, seq, to)
						}
					}
				}
				d.assertClosed(t)
			})
		})
	}
}

func TestIMAPProvider_SearchAndExtract_MailWithoutCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const addr = "aaaaaaaaaaaa@example.com"
		mb := &sharedMailbox{msgs: []mailboxMessage{{to: addr}}}
		c := pipeClient(t, "IMAP4rev1", mb.session())
		if _, err := c.Select("INBOX", false); err != nil {
			t.Fatalf("Select: %v", err)
		}
		got, err := (&IMAPProvider{}).searchAndExtract(c, addr, time.Time{})
		if got != "" || err != nil {
			t.Errorf("searchAndExtract = %q, %v, want no code and no error", got, err)
		}
		if fetched := mb.fetchedSince(0); !slices.Equal(fetched, []uint32{1}) {
			t.Errorf("fetched messages %v, want [1]", fetched)
		}
	})
}

// barrierIDLESession serves a server that sends on idled when IDLE starts. It holds
// the IDLE until release closes, then sends EXISTS, and SEARCH finds the mail.
func barrierIDLESession(idled chan<- struct{}, release <-chan struct{}) serveFunc {
	var idleTag string
	arrived := false
	return func(w io.Writer, line string) {
		if line == "DONE" {
			_, _ = fmt.Fprintf(w, "%s OK idle done\r\n", idleTag)
			return
		}
		tag, name := parseCommand(line)
		switch {
		case name == "IDLE":
			idleTag = tag
			_, _ = io.WriteString(w, "+ idling\r\n")
			idled <- struct{}{}
			<-release
			arrived = true
			_, _ = io.WriteString(w, "* 1 EXISTS\r\n")
			return
		case name == "SEARCH" && arrived:
			_, _ = io.WriteString(w, "* SEARCH 1\r\n")
		case name == "SEARCH":
			_, _ = io.WriteString(w, "* SEARCH\r\n")
		case name == "FETCH":
			msg := verificationMessage()
			_, _ = fmt.Fprintf(w, "* 1 FETCH (BODY[] {%d}\r\n%s)\r\n", len(msg), msg)
		}
		okIdle(w, line)
	}
}

func TestIMAPProvider_ConcurrentWaits(t *testing.T) {
	// Each wait holds its connection through the IDLE, so all of them are in IDLE
	// at once only when the waits of one provider share no lock and no connection.
	synctest.Test(t, func(t *testing.T) {
		const numWorkers = 4
		idled := make(chan struct{}, numWorkers)
		release := make(chan struct{})
		d := &fakeDialer{greeting: idleGreeting, session: func() serveFunc { return barrierIDLESession(idled, release) }}
		p := &IMAPProvider{PollInterval: time.Hour, Logger: discardLogger(), dialFn: d.dial}

		codes := make([]string, numWorkers)
		errs := make([]error, numWorkers)
		var wg sync.WaitGroup
		for i := range numWorkers {
			wg.Go(func() {
				codes[i], errs[i] = p.WaitForVerificationCode(t.Context(), "test@example.com", time.Time{}, time.Minute)
			})
		}
		inIdle := 0
		guard := time.After(2 * time.Second)
	barrier:
		for inIdle < numWorkers {
			select {
			case <-idled:
				inIdle++
			case <-guard:
				break barrier
			}
		}
		close(release)
		wg.Wait()

		if inIdle != numWorkers {
			t.Errorf("%d of %d waits were in IDLE at once after 2s, want all", inIdle, numWorkers)
		}
		for i := range numWorkers {
			if codes[i] != "ABC123" || errs[i] != nil {
				t.Errorf("worker %d: WaitForVerificationCode = %q, %v, want %q", i, codes[i], errs[i], "ABC123")
			}
		}
		if got := d.dialCount(); got != numWorkers {
			t.Errorf("dials = %d, want %d", got, numWorkers)
		}
	})
}

func TestVerificationCodeRe(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard code",
			input: `<span data-testid="registration-started-verification-code" class="code">ABC123</span>`,
			want:  "ABC123",
		},
		{
			name:  "no match",
			input: `<span>no code here</span>`,
			want:  "",
		},
		{
			name:  "numeric only",
			input: `<td data-testid="registration-started-verification-code">123456</td>`,
			want:  "123456",
		},
		{
			name:  "extra attributes before testid",
			input: `<span class="verify" data-testid="registration-started-verification-code" style="font-weight:bold">QR5T8X</span>`,
			want:  "QR5T8X",
		},
		{
			name:  "whitespace around code",
			input: `<span data-testid="registration-started-verification-code">  XYZ789  </span>`,
			want:  "",
		},
		{
			name:  "code immediately after closing bracket",
			input: `<div data-testid="registration-started-verification-code">HELLO1</div>`,
			want:  "HELLO1",
		},
		{
			name:  "wrong testid value",
			input: `<span data-testid="some-other-testid">ABC123</span>`,
			want:  "",
		},
		{
			name:  "multiline html with newline in attributes",
			input: "<span\ndata-testid=\"registration-started-verification-code\"\n>MN42PQ</span>",
			want:  "MN42PQ",
		},
		{
			name:  "alpha only code",
			input: `<span data-testid="registration-started-verification-code">ABCDEF</span>`,
			want:  "ABCDEF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := verificationCodeRe.FindSubmatch([]byte(tt.input))
			var got string
			if len(matches) >= 2 {
				got = string(matches[1])
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
