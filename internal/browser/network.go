package browser

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/network"

	"jagex_account_creator/internal/bytesize"
)

// resourceState names how a request ended, or that it was still open at Close.
type resourceState string

// The states of a resourceEntry. They are mutually exclusive.
const (
	stateFinished resourceState = "finished"
	stateRedirect resourceState = "redirect"
	stateFailed   resourceState = "failed"
	stateBlocked  resourceState = "blocked"
	stateOpen     resourceState = "open"
)

// resourceEntry records the bytes received for one network request, or for
// one redirect hop of it.
type resourceEntry struct {
	URL       string
	Type      string        // ResourceType from CDP (Document, Script, Stylesheet, Image, Font, XHR, Fetch, etc.)
	Bytes     int64         // encodedDataLength of the finish or the redirect response, else the DataReceived sum
	State     resourceState // how the request ended
	FromCache bool          // True if served from the disk cache (no network transfer)
}

// networkSummary holds a breakdown of browser network traffic.
type networkSummary struct {
	Resources    []resourceEntry  // all resources, sorted largest first
	ByDomain     map[string]int64 // total bytes per domain
	ByType       map[string]int64 // total bytes per resource type
	CacheHits    int              // number of resources served from disk cache
	Received     int64            // total bytes of all resources
	Sent         int64            // estimated total bytes sent
	AttachErrors []error          // child targets that the attacher could not set up or resume
}

// NetworkTracker tracks the bytes that the browser sends and receives over
// http: and https:. It reads the CDP Network events of the page and of each
// child target that a TargetAttacher passes to it. Create it before the first
// request of the page.
type NetworkTracker struct {
	wg        sync.WaitGroup // every pump, and every subscribe in progress
	closeOnce sync.Once

	mu           sync.Mutex
	closed       bool
	cancels      []context.CancelFunc // one for each subscribed client, to close its streams
	sent         int64
	requests     map[network.RequestID]requestMeta   // open requests of every subscribed client
	early        map[network.RequestID]resourceEntry // ends that came before their request, with no URL yet
	ended        map[network.RequestID]int           // the index in resources of each ended request
	skipped      map[network.RequestID]struct{}      // requests that do not go over the network
	resources    []resourceEntry                     // ended requests and redirect hops
	attachErrors []error
}

// requestMeta is an open request. A meta with no URL holds the bytes that
// another target reported before the request arrived.
type requestMeta struct {
	url          string
	resourceType string
	fromCache    bool
	received     int64 // sum of DataReceived.encodedDataLength
}

// entry returns the resource entry of the request, with its state and bytes.
func (m requestMeta) entry(state resourceState, bytes int64) resourceEntry {
	return resourceEntry{URL: m.url, Type: m.resourceType, Bytes: bytes, State: state, FromCache: m.fromCache}
}

// NewNetworkTracker subscribes to the Network events of the page client c, and
// starts the background goroutine that records them. Call it before the first
// request of c, because it can lose an event that comes before it returns.
func NewNetworkTracker(ctx context.Context, c *cdp.Client) (*NetworkTracker, error) { // coverage-ignore: needs a live Chrome
	t := &NetworkTracker{
		requests: make(map[network.RequestID]requestMeta),
		early:    make(map[network.RequestID]resourceEntry),
		ended:    make(map[network.RequestID]int),
		skipped:  make(map[network.RequestID]struct{}),
	}
	if err := t.track(ctx, c.Network); err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

// errTrackerClosed is the error of a subscribe after Close.
var errTrackerClosed = errors.New("the network tracker is closed")

// track subscribes to the Network events of n until Close or a cancel of ctx.
func (t *NetworkTracker) track(ctx context.Context, n cdp.Network) error {
	ctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cancel()
		return errTrackerClosed
	}
	t.cancels = append(t.cancels, cancel)
	// The hold keeps the wait of Close from starting before subscribe adds its pump.
	t.wg.Add(1)
	t.mu.Unlock()
	defer t.wg.Done()
	return t.subscribe(ctx, n)
}

// addAttachError adds err to the attach errors of the breakdown.
func (t *NetworkTracker) addAttachError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attachErrors = append(t.attachErrors, err)
}

// subscribe opens the Network event streams of n, orders them with cdp.Sync,
// and records them in one goroutine. A cancel of ctx closes the streams, also
// after an error.
func (t *NetworkTracker) subscribe(ctx context.Context, n cdp.Network) error { // coverage-ignore: needs a live Chrome
	requestWillBeSent, err := n.RequestWillBeSent(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to RequestWillBeSent: %w", err)
	}
	responseReceived, err := n.ResponseReceived(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to ResponseReceived: %w", err)
	}
	dataReceived, err := n.DataReceived(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to DataReceived: %w", err)
	}
	loadingFinished, err := n.LoadingFinished(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to LoadingFinished: %w", err)
	}
	loadingFailed, err := n.LoadingFailed(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to LoadingFailed: %w", err)
	}
	// Unordered streams can deliver a finish before its request, and lose the entry.
	if err := cdp.Sync(requestWillBeSent, responseReceived, dataReceived, loadingFinished, loadingFailed); err != nil {
		return fmt.Errorf("ordering the Network streams: %w", err)
	}

	t.wg.Go(func() {
		pumpSynced([]syncedStream{
			synced(requestWillBeSent, t.recordRequest),
			synced(responseReceived, t.recordResponse),
			synced(dataReceived, t.recordData),
			synced(loadingFinished, t.recordFinished),
			synced(loadingFailed, t.recordFailed),
		})
	})
	return nil
}

// isNetworkURL reports whether rawURL goes over the network. data:, blob: and
// about: URLs cost no proxy bytes, so the tracker skips them.
func isNetworkURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://")
}

// truncateURL cuts u to n characters and marks the cut.
func truncateURL(u string, n int) string {
	if len(u) > n {
		return u[:n] + "..."
	}
	return u
}

// fromDiskCache reports whether the disk cache served r.
func fromDiskCache(r *network.Response) bool {
	return r.FromDiskCache != nil && *r.FromDiskCache
}

// requestSize estimates the bytes that r sends: its URL, its headers as JSON
// and its body.
func requestSize(r *network.Request) int64 {
	size := int64(len(r.URL)) + int64(len(r.Headers))
	for _, entry := range r.PostDataEntries {
		if entry.Bytes != nil {
			size += postBodyLen(*entry.Bytes)
		}
	}
	return size
}

// postBodyLen returns the length of a post data entry. CDP sends the entry as
// base64. An entry that is not base64 counts at its raw length.
func postBodyLen(encoded string) int64 {
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return int64(len(encoded))
	}
	return int64(len(body))
}

// addLocked appends e to the resource list. t.mu must be held.
func (t *NetworkTracker) addLocked(e resourceEntry) {
	t.resources = append(t.resources, e)
}

// openLocked returns the meta of the open request id. A child target can report
// a request that its parent sent, so an unknown request gets a meta with no URL.
// ok is false for a skipped or ended request. t.mu must be held.
func (t *NetworkTracker) openLocked(id network.RequestID) (requestMeta, bool) {
	if meta, ok := t.requests[id]; ok {
		return meta, true
	}
	// The renderer can report DataReceived after the finish, and the finish already counts those bytes.
	_, skipped := t.skipped[id]
	_, early := t.early[id]
	_, ended := t.ended[id]
	return requestMeta{}, !skipped && !early && !ended
}

// addEndLocked appends the entry e of the ended request id. t.mu must be held.
func (t *NetworkTracker) addEndLocked(id network.RequestID, e resourceEntry) {
	t.ended[id] = len(t.resources)
	t.addLocked(e)
}

// endLocked ends the open request id with state and bytes. An end that came
// before its request waits in t.early for the URL. t.mu must be held.
func (t *NetworkTracker) endLocked(id network.RequestID, meta requestMeta, state resourceState, bytes int64) {
	delete(t.requests, id)
	if meta.url == "" {
		t.early[id] = meta.entry(state, bytes)
		return
	}
	t.addEndLocked(id, meta.entry(state, bytes))
}

// startLocked maps request id to meta. The events that another target reported
// before the request carry over. t.mu must be held.
func (t *NetworkTracker) startLocked(id network.RequestID, meta requestMeta) {
	if end, ok := t.early[id]; ok {
		delete(t.early, id)
		end.URL, end.Type = meta.url, meta.resourceType
		t.addEndLocked(id, end)
		return
	}
	// A redirect replaces the meta of its last hop, which has a URL.
	if prev := t.requests[id]; prev.url == "" {
		meta.received, meta.fromCache = prev.received, prev.fromCache
	}
	t.requests[id] = meta
}

// recordRequest records a redirect hop, estimates the sent bytes and maps the
// request ID to the new URL.
func (t *NetworkTracker) recordRequest(event *network.RequestWillBeSentReply) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Request.URL is the post-redirect URL. The hop's bytes belong to the URL that redirected.
	if r := event.RedirectResponse; r != nil {
		t.addLocked(resourceEntry{
			URL:       truncateURL(r.URL, 200),
			Type:      string(event.Type),
			Bytes:     int64(r.EncodedDataLength),
			State:     stateRedirect,
			FromCache: fromDiskCache(r),
		})
	}

	id := event.RequestID
	if !isNetworkURL(event.Request.URL) {
		// Another target can report the end of the request before or after it.
		delete(t.requests, id)
		delete(t.early, id)
		t.skipped[id] = struct{}{}
		return
	}
	t.sent += requestSize(&event.Request)
	t.startLocked(id, requestMeta{
		url:          truncateURL(event.Request.URL, 200),
		resourceType: string(event.Type),
	})
}

// recordResponse tracks cache status from response headers. The page can report
// the response of a request after a child target reported its end.
func (t *NetworkTracker) recordResponse(event *network.ResponseReceivedReply) {
	if !fromDiskCache(&event.Response) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	id := event.RequestID
	if i, ok := t.ended[id]; ok {
		t.resources[i].FromCache = true
		return
	}
	if end, ok := t.early[id]; ok {
		end.FromCache = true
		t.early[id] = end
		return
	}
	if meta, ok := t.openLocked(id); ok {
		meta.fromCache = true
		t.requests[id] = meta
	}
}

// recordData adds a received chunk to the sum of its request.
func (t *NetworkTracker) recordData(event *network.DataReceivedReply) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if meta, ok := t.openLocked(event.RequestID); ok {
		meta.received += int64(event.EncodedDataLength)
		t.requests[event.RequestID] = meta
	}
}

// recordFinished ends a request with the total bytes of its finish.
func (t *NetworkTracker) recordFinished(event *network.LoadingFinishedReply) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if meta, ok := t.openLocked(event.RequestID); ok {
		t.endLocked(event.RequestID, meta, stateFinished, int64(event.EncodedDataLength))
	}
}

// recordFailed ends a request with the bytes it received before it failed.
// A request that the block list stopped fails with a BlockedReason.
func (t *NetworkTracker) recordFailed(event *network.LoadingFailedReply) {
	state := stateFailed
	if event.BlockedReason != "" {
		state = stateBlocked
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if meta, ok := t.openLocked(event.RequestID); ok {
		t.endLocked(event.RequestID, meta, state, meta.received)
	}
}

// summary returns a breakdown of network traffic by resource, domain, and type.
// Must be called after Close() to ensure all events have been processed.
func (t *NetworkTracker) summary() networkSummary {
	t.mu.Lock()
	defer t.mu.Unlock()

	summary := networkSummary{
		Resources:    slices.Clone(t.resources),
		ByDomain:     make(map[string]int64),
		ByType:       make(map[string]int64),
		Sent:         t.sent,
		AttachErrors: slices.Clone(t.attachErrors),
	}

	// Sort largest first. The URL orders equal sizes, so the log order is stable.
	slices.SortFunc(summary.Resources, func(a, b resourceEntry) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), strings.Compare(a.URL, b.URL))
	})

	for _, r := range t.resources {
		summary.Received += r.Bytes
		summary.ByType[r.Type] += r.Bytes
		if u, err := url.Parse(r.URL); err == nil {
			summary.ByDomain[u.Host] += r.Bytes
		}
		if r.FromCache {
			summary.CacheHits++
		}
	}

	return summary
}

// Close stops tracking and waits for background goroutines to exit. A subscribe
// after Close fails. Each request still open keeps the bytes it received, and
// an end with no request keeps its bytes with no URL. Safe to call multiple
// times, and on a nil tracker.
func (t *NetworkTracker) Close() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		cancels := t.cancels
		t.mu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
		t.wg.Wait()

		t.mu.Lock()
		defer t.mu.Unlock()
		for id, meta := range t.requests {
			t.addLocked(meta.entry(stateOpen, meta.received))
			delete(t.requests, id)
		}
		for id, end := range t.early {
			t.addLocked(end)
			delete(t.early, id)
		}
	})
}

// LogSummary logs a DEBUG breakdown of browser network traffic by type, domain
// and resource. Its CDP totals are estimates, so no record uses them. A nil
// tracker logs nothing.
func (t *NetworkTracker) LogSummary(logger *slog.Logger) {
	if t == nil {
		return
	}
	summary := t.summary()

	// Each size is an exact byte count, so the residual against the proxy count is exact.
	// The size attributes are the same counts in a human-readable form.
	logger.Debug("browser bandwidth breakdown",
		"total_recv", summary.Received,
		"total_recv_size", bytesize.Format(summary.Received),
		"total_sent", summary.Sent,
		"total_sent_size", bytesize.Format(summary.Sent),
		"resources", len(summary.Resources),
		"cache_hits", summary.CacheHits,
	)

	// Log by resource type.
	for typ, bytes := range summary.ByType {
		if typ == "" {
			typ = "unknown"
		}
		logger.Debug("bandwidth by type", "type", typ, "bytes", bytes, "size", bytesize.Format(bytes))
	}

	// Log by domain.
	// Limitation: the sum omits TLS, HTTP/2 framing and a preconnect socket that no
	// request uses, so the proxy count of each host is some KB higher.
	for domain, bytes := range summary.ByDomain {
		logger.Debug("bandwidth by domain", "domain", domain, "bytes", bytes, "size", bytesize.Format(bytes))
	}

	// The bytes of a target in this list are missing or the target stalled.
	for _, err := range summary.AttachErrors {
		logger.Debug("child target attach failed", "error", err)
	}

	// Log every resource, so that each byte has an entry.
	for i, r := range summary.Resources {
		logger.Debug("resource",
			"rank", i+1,
			"bytes", r.Bytes,
			"size", bytesize.Format(r.Bytes),
			"state", string(r.State),
			"cached", r.FromCache,
			"type", r.Type,
			"url", truncateURL(r.URL, 120),
		)
	}
}
