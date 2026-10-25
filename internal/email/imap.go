package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// verificationCodeRe matches the Jagex registration verification code in the email HTML.
var verificationCodeRe = regexp.MustCompile(
	`data-testid="registration-started-verification-code"[^>]*>([A-Z0-9]+)<`,
)

// IMAPProvider implements Provider using an IMAP mailbox.
// Each wait opens its own IMAP connection, so the provider is safe for concurrent use.
type IMAPProvider struct {
	Host         string // IMAP server hostname
	Port         int    // IMAP server port (typically 993 for TLS)
	User         string // IMAP login username (full email)
	Pass         string // IMAP login password
	Domain       string // Domain of the mailbox addresses
	PollInterval time.Duration
	Logger       *slog.Logger

	dialFn func(ctx context.Context, addr string) (net.Conn, error) // test override
}

// Address returns local at the configured domain.
func (p *IMAPProvider) Address(local string) string {
	return local + "@" + p.Domain
}

// pollInterval returns PollInterval, or 2s when it is zero.
func (p *IMAPProvider) pollInterval() time.Duration {
	if p.PollInterval == 0 {
		return 2 * time.Second
	}
	return p.PollInterval
}

// WaitForVerificationCode waits for a Jagex verification email to address and
// returns its code. After a failed IMAP wait it tries again, until timeout ends.
// A refused login returns at once.
func (p *IMAPProvider) WaitForVerificationCode(ctx context.Context, address string, since time.Time, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, ErrEmailTimeout)
	defer cancel()

	logger := p.Logger.With("email", address)
	var lastErr error
	for {
		code, err := p.waitIDLE(ctx, address, since, logger)
		switch {
		case err == nil:
			return code, nil
		case ctx.Err() != nil:
			// The end of ctx can cause err, so err is not kept as the last IMAP error.
			return "", waitError(ctx, address, lastErr)
		case errors.Is(err, ErrEmailLogin):
			return "", fmt.Errorf("waiting for verification email to %s: %w", address, err)
		}
		lastErr = err
		logger.Debug("IMAP wait failed, trying again", "error", err)
		select {
		case <-ctx.Done():
			return "", waitError(ctx, address, lastErr)
		case <-time.After(p.pollInterval()):
		}
	}
}

// waitError returns the error of a wait that ctx ended, with the last IMAP error when there is one.
func waitError(ctx context.Context, address string, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("waiting for verification email to %s: %w (last IMAP error: %w)", address, context.Cause(ctx), lastErr)
	}
	return fmt.Errorf("waiting for verification email to %s: %w", address, context.Cause(ctx))
}

// waitIDLE uses IMAP IDLE to wait for mailbox changes, checking for the
// verification email after each notification. If the server doesn't support
// IDLE, the go-imap library falls back to NOOP polling automatically.
func (p *IMAPProvider) waitIDLE(ctx context.Context, address string, since time.Time, logger *slog.Logger) (string, error) {
	c, closeConn, err := p.connect(ctx)
	if err != nil {
		return "", err
	}
	defer closeConn()

	// Limitation: this write can race go-imap's reader if an untagged response arrives after LOGIN.
	updates := make(chan client.Update, 64)
	c.Updates = updates

	// Select INBOX (read-write for better IDLE compatibility).
	if _, err := c.Select("INBOX", false); err != nil {
		return "", fmt.Errorf("selecting INBOX for IDLE: %w", err)
	}
	// Select returns after the reader delivered SELECT's own EXISTS and RECENT,
	// so the drain drops only those, and the search below finds any mail they report.
	drainUpdates(updates)

	pollInterval := p.pollInterval()
	code, err := p.searchAndExtract(c, address, since)
	for code == "" && err == nil {
		var changed bool
		changed, err = idleOnce(ctx, c, updates, pollInterval)
		if changed {
			logger.Debug("IDLE notified of mailbox change")
			code, err = p.searchAndExtract(c, address, since)
		}
	}
	return code, err
}

// idleOnce runs one IDLE round and reports whether the mailbox may have changed.
// A round that returns no error has drained the pending updates.
func idleOnce(ctx context.Context, c *client.Client, updates <-chan client.Update, pollInterval time.Duration) (bool, error) {
	// Each IDLE command runs under c.Timeout. A restart at half of it renews that bound,
	// so the DONE exchange always has at least half of it.
	opts := &client.IdleOptions{
		LogoutTimeout: c.Timeout / 2,
		PollInterval:  pollInterval,
	}
	stop := make(chan struct{})
	idleDone := make(chan error, 1)
	go func() {
		idleDone <- c.Idle(stop, opts)
	}()

	// Wait for a mailbox update, IDLE error, or context cancellation.
	var gotMailboxUpdate bool
	select {
	case <-ctx.Done():
		close(stop)
		<-idleDone //nolint:errcheck // The end of ctx closes the connection, so ctx.Err() is the cause of any IDLE error.
		return false, ctx.Err()
	case update := <-updates:
		close(stop)
		if err := <-idleDone; err != nil {
			return false, fmt.Errorf("IDLE: %w", err)
		}
		if _, ok := update.(*client.MailboxUpdate); ok {
			gotMailboxUpdate = true
		}
	case err := <-idleDone:
		close(stop) // safe even though goroutine already returned
		if err != nil {
			return false, fmt.Errorf("IDLE: %w", err)
		}
		// IDLE ended cleanly without stop — treat as potential update.
		gotMailboxUpdate = true
	}

	// Drain remaining updates.
	drainUpdates(updates)
	return gotMailboxUpdate, nil
}

// drainUpdates drains all pending updates from the channel.
func drainUpdates(ch <-chan client.Update) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// CheckLogin connects to the IMAP server, logs in and logs out. It returns an
// error that matches ErrEmailLogin when the server refuses the login.
func (p *IMAPProvider) CheckLogin(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, imapCommandTimeout)
	defer cancel()

	_, closeConn, err := p.connect(ctx)
	if err != nil {
		return err
	}
	closeConn()
	return nil
}

// imapCommandTimeout is the per-command timeout for IMAP operations.
const imapCommandTimeout = 30 * time.Second

// connect dials the server and logs in. The end of ctx closes the connection, so ctx
// bounds every command on it. The returned func logs out and closes the connection.
func (p *IMAPProvider) connect(ctx context.Context) (*client.Client, func(), error) {
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	var netConn net.Conn
	var err error
	if p.dialFn != nil {
		netConn, err = p.dialFn(ctx, addr)
	} else {
		netConn, err = (&tls.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to IMAP: %w", err)
	}

	stop := context.AfterFunc(ctx, func() { _ = netConn.Close() })
	c, err := client.New(netConn)
	if err != nil {
		if !stop() {
			return nil, nil, fmt.Errorf("IMAP connection: %w", ctx.Err())
		}
		_ = netConn.Close()
		return nil, nil, fmt.Errorf("initializing IMAP client: %w", err)
	}
	// Limitation: client.New starts the reader first, so a reader error before this line goes to stderr.
	c.ErrorLog = slog.NewLogLogger(p.Logger.Handler(), slog.LevelDebug)
	c.Timeout = imapCommandTimeout

	if err := c.Login(p.User, p.Pass); err != nil {
		if !stop() {
			return nil, nil, fmt.Errorf("IMAP connection: %w", ctx.Err())
		}
		_ = c.Terminate()
		// Limitation: a transport failure inside LOGIN also reads as a refused login, which costs one attempt.
		return nil, nil, fmt.Errorf("%w: %w", ErrEmailLogin, err)
	}

	closeConn := func() {
		// Logout runs while ctx still bounds it. Terminate then closes the socket when the server sent no BYE.
		_ = c.Logout()
		stop()
		_ = c.Terminate()
	}
	return c, closeConn, nil
}

// searchAndExtract returns the verification code in the unseen mail to
// address. IMAP SENTSINCE takes a date, so the search matches mail from the
// date of since on. Each attempt has its own address and asks for one code, so
// it takes the first message with a code. Assumes INBOX is already selected.
// The address is in the search so the fetch touches only this worker's mail.
// BODY[] sets \Seen, and the other workers search UNSEEN.
// Limitation: the server compares the date of the mail's Date header in that
// header's zone. A mail stamped behind UTC just after UTC midnight fails toward
// a timeout.
func (p *IMAPProvider) searchAndExtract(c *client.Client, address string, since time.Time) (string, error) {
	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	criteria.SentSince = since
	criteria.Header.Add("To", address)

	uids, err := c.Search(criteria)
	if err != nil {
		return "", fmt.Errorf("searching INBOX: %w", err)
	}
	if len(uids) == 0 {
		return "", nil
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids...)

	messages := make(chan *imap.Message, len(uids))
	section := &imap.BodySectionName{}
	if err := c.Fetch(seqSet, []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope}, messages); err != nil {
		return "", fmt.Errorf("fetching messages: %w", err)
	}

	for msg := range messages {
		if !addressedTo(msg, address) {
			continue
		}
		if code, ok := codeFromMessage(msg, section); ok {
			return code, nil
		}
	}
	return "", nil
}

// addressedTo reports whether msg is addressed to address. A message without an envelope matches.
func addressedTo(msg *imap.Message, address string) bool {
	if msg.Envelope == nil {
		return true
	}
	for _, to := range msg.Envelope.To {
		if strings.EqualFold(to.Address(), address) {
			return true
		}
	}
	return false
}

// codeFromMessage returns the verification code in the body section of msg.
func codeFromMessage(msg *imap.Message, section *imap.BodySectionName) (string, bool) {
	body := msg.GetBody(section)
	if body == nil {
		return "", false
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		return "", false
	}

	decoded, err := decodeMessageBody(raw)
	if err != nil {
		// MIME decode failed; fall back to raw bytes.
		decoded = raw
	}

	matches := verificationCodeRe.FindSubmatch(decoded)
	if len(matches) >= 2 {
		return string(matches[1]), true
	}
	return "", false
}
