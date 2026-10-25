package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/network"
)

// jsdMarker is in the URL of the Cloudflare JavaScript Detections (JSD) report.
// Limitation: if Cloudflare renames the report path, the gate waits until its timeout and fails with ErrChallengeTimeout.
const jsdMarker = "/jsd/"

// clearanceSource names the response that set a cf_clearance cookie.
type clearanceSource string

// The sources of a cf_clearance cookie. sourceNone means that no cf_clearance was set.
const (
	sourceNone      clearanceSource = ""
	sourceJSD       clearanceSource = "jsd"
	sourceChallenge clearanceSource = "challenge"
)

// classifyClearance returns the source of the cf_clearance cookie that the
// response headers of rawURL set, or sourceNone when they set none.
func classifyClearance(rawURL string, headers network.Headers) clearanceSource {
	var fields map[string]string
	if err := json.Unmarshal(headers, &fields); err != nil {
		return sourceNone
	}
	for key, value := range fields {
		if strings.EqualFold(key, "set-cookie") && setsClearance(value) {
			if strings.Contains(rawURL, jsdMarker) {
				return sourceJSD
			}
			// Limitation: any non-JSD URL that sets cf_clearance counts as the challenge. If one appears, the HTTP phase fails loud with 403.
			return sourceChallenge
		}
	}
	return sourceNone
}

// setsClearance reports whether a Set-Cookie value sets cf_clearance. CDP
// joins repeated Set-Cookie headers with a newline.
func setsClearance(value string) bool {
	for line := range strings.SplitSeq(value, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "cf_clearance=") {
			return true
		}
	}
	return false
}

// ClearanceGate watches the responses of a page for two cf_clearance cookies.
// The JSD report sets one and the managed challenge sets the other. Each one
// carries a pass that the HTTP client cannot earn.
type ClearanceGate struct {
	c              *cdp.Client
	logger         *slog.Logger
	challengeTitle string // the site's own title for its challenge page
	start          time.Time
	cancel         context.CancelFunc
	wg             sync.WaitGroup // the pump
	closeOnce      sync.Once
	done           chan struct{} // closed when both sources are seen, so Wait wakes without a poll

	// Only the pump goroutine touches urls and seen.
	urls map[network.RequestID]string // the last URL of each request
	seen map[clearanceSource]bool
}

// newClearanceGate returns a gate with no listeners. cancel stops its listeners.
func newClearanceGate(c *cdp.Client, logger *slog.Logger, challengeTitle string, cancel context.CancelFunc) *ClearanceGate {
	return &ClearanceGate{
		c:              c,
		logger:         logger,
		challengeTitle: challengeTitle,
		start:          time.Now(),
		cancel:         cancel,
		done:           make(chan struct{}),
		urls:           make(map[network.RequestID]string),
		seen:           make(map[clearanceSource]bool),
	}
}

// NewClearanceGate starts to watch the responses of c. Call it before the
// navigation, because the JSD report can come before the load event.
// challengeTitle is the site's own title for its challenge page. The caller
// must call Close.
func NewClearanceGate(ctx context.Context, c *cdp.Client, logger *slog.Logger, challengeTitle string) (*ClearanceGate, error) { // coverage-ignore: needs a live Chrome
	gateCtx, cancel := context.WithCancel(ctx)
	g := newClearanceGate(c, logger, challengeTitle, cancel)
	if err := g.subscribe(gateCtx, c.Network); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

// subscribe opens the request and the response streams of n, orders them with
// cdp.Sync, and records them in one goroutine. A cancel of ctx closes the
// streams, also after an error.
func (g *ClearanceGate) subscribe(ctx context.Context, n cdp.Network) error { // coverage-ignore: needs a live Chrome
	requestWillBeSent, err := n.RequestWillBeSent(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to RequestWillBeSent: %w", err)
	}
	responseExtraInfo, err := n.ResponseReceivedExtraInfo(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to ResponseReceivedExtraInfo: %w", err)
	}
	// A response finds its URL through its request, so the request must be recorded first.
	if err := cdp.Sync(requestWillBeSent, responseExtraInfo); err != nil {
		return fmt.Errorf("ordering the Network streams: %w", err)
	}

	g.wg.Go(func() {
		pumpSynced([]syncedStream{
			synced(requestWillBeSent, g.recordRequest),
			synced(responseExtraInfo, g.recordResponse),
		})
	})
	return nil
}

// recordRequest maps the request ID to its URL. A redirect hop keeps the ID,
// so the map holds the URL of the last hop.
func (g *ClearanceGate) recordRequest(event *network.RequestWillBeSentReply) {
	g.urls[event.RequestID] = event.Request.URL
}

// recordResponse marks the source of a cf_clearance that the response sets. It
// logs the first time that it sees each source. It closes done when both
// sources are first seen, in either order.
func (g *ClearanceGate) recordResponse(event *network.ResponseReceivedExtraInfoReply) {
	source := classifyClearance(g.urls[event.RequestID], event.Headers)
	if source == sourceNone || g.seen[source] {
		return
	}
	g.seen[source] = true
	g.logger.Debug("cf_clearance source seen", "source", string(source), "since_start", time.Since(g.start).Round(time.Millisecond))
	if g.seen[sourceJSD] && g.seen[sourceChallenge] {
		close(g.done)
	}
}

// Close stops the listeners and waits for the pump to exit. It is safe to
// call more than once.
func (g *ClearanceGate) Close() {
	g.closeOnce.Do(func() {
		g.cancel()
		g.wg.Wait()
	})
}
