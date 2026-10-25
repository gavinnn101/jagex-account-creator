package browser

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/network"

	"jagex_account_creator/internal/logtest"
)

func newTestTracker() *NetworkTracker {
	return &NetworkTracker{
		requests: make(map[network.RequestID]requestMeta),
		early:    make(map[network.RequestID]resourceEntry),
		ended:    make(map[network.RequestID]int),
		skipped:  make(map[network.RequestID]struct{}),
	}
}

func TestIsNetworkURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://example.com/", true},
		{"https://example.com/", true},
		{"data:font/woff2;base64,AAAA", false},
		{"blob:https://example.com/0f1e", false},
		{"about:blank", false},
		{"chrome://new-tab-page/", false},
	}
	for _, tt := range tests {
		if got := isNetworkURL(tt.url); got != tt.want {
			t.Errorf("isNetworkURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestPostBodyLen(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		want    int64
	}{
		{name: "length a multiple of 3", encoded: "YWJj", want: 3},
		{name: "two padding characters", encoded: "YWJjZA==", want: 4},
		{name: "one padding character", encoded: "YWJjZGU=", want: 5},
		{name: "empty", encoded: "", want: 0},
		{name: "not base64", encoded: "a=1&b=2", want: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postBodyLen(tt.encoded); got != tt.want {
				t.Errorf("postBodyLen(%q) = %d, want %d", tt.encoded, got, tt.want)
			}
		})
	}
}

func TestRecordRequest(t *testing.T) {
	body := "YWJjZA==" // "abcd"
	long := "https://example.com/" + strings.Repeat("x", 200)
	old := requestMeta{url: "https://example.com/start", resourceType: "Document", fromCache: true, received: 50}
	tests := []struct {
		name          string
		event         network.RequestWillBeSentReply
		wantSent      int64
		wantRequests  map[network.RequestID]requestMeta
		wantResources []resourceEntry
	}{
		{
			name:         "headers and decoded post data",
			event:        network.RequestWillBeSentReply{RequestID: "r1", Type: "XHR", Request: network.Request{URL: "https://example.com/", Headers: network.Headers(`{"a":"b"}`), PostDataEntries: []network.PostDataEntry{{Bytes: &body}, {}}}},
			wantSent:     20 + 9 + 4,
			wantRequests: map[network.RequestID]requestMeta{"old": old, "r1": {url: "https://example.com/", resourceType: "XHR"}},
		},
		{
			name:         "http URL",
			event:        network.RequestWillBeSentReply{RequestID: "r1", Type: "Script", Request: network.Request{URL: "http://example.com/a"}},
			wantSent:     20,
			wantRequests: map[network.RequestID]requestMeta{"old": old, "r1": {url: "http://example.com/a", resourceType: "Script"}},
		},
		{
			name:         "URL of 200 characters",
			event:        network.RequestWillBeSentReply{RequestID: "r1", Type: "Script", Request: network.Request{URL: long[:200]}},
			wantSent:     200,
			wantRequests: map[network.RequestID]requestMeta{"old": old, "r1": {url: long[:200], resourceType: "Script"}},
		},
		{
			name:         "URL of 201 characters",
			event:        network.RequestWillBeSentReply{RequestID: "r1", Type: "Script", Request: network.Request{URL: long[:201]}},
			wantSent:     201,
			wantRequests: map[network.RequestID]requestMeta{"old": old, "r1": {url: long[:200] + "...", resourceType: "Script"}},
		},
		{
			name:         "data URL",
			event:        network.RequestWillBeSentReply{RequestID: "r1", Type: "Font", Request: network.Request{URL: "data:font/woff2;base64,AAAA"}},
			wantRequests: map[network.RequestID]requestMeta{"old": old},
		},
		{
			name: "redirect hop",
			event: network.RequestWillBeSentReply{
				RequestID:        "old",
				Type:             "Document",
				Request:          network.Request{URL: "https://example.com/after"},
				RedirectResponse: &network.Response{URL: "https://example.com/before", EncodedDataLength: 2181},
			},
			wantSent:      25,
			wantRequests:  map[network.RequestID]requestMeta{"old": {url: "https://example.com/after", resourceType: "Document"}},
			wantResources: []resourceEntry{{URL: "https://example.com/before", Type: "Document", Bytes: 2181, State: stateRedirect}},
		},
		{
			name: "redirect hop from the disk cache",
			event: network.RequestWillBeSentReply{
				RequestID:        "old",
				Type:             "Document",
				Request:          network.Request{URL: "https://example.com/after"},
				RedirectResponse: &network.Response{URL: "https://example.com/before", EncodedDataLength: 0, FromDiskCache: new(true)},
			},
			wantSent:      25,
			wantRequests:  map[network.RequestID]requestMeta{"old": {url: "https://example.com/after", resourceType: "Document"}},
			wantResources: []resourceEntry{{URL: "https://example.com/before", Type: "Document", State: stateRedirect, FromCache: true}},
		},
		{
			name: "redirect to a URL that is not http",
			event: network.RequestWillBeSentReply{
				RequestID:        "old",
				Type:             "Document",
				Request:          network.Request{URL: "about:blank"},
				RedirectResponse: &network.Response{URL: "https://example.com/before", EncodedDataLength: 300},
			},
			wantRequests:  map[network.RequestID]requestMeta{},
			wantResources: []resourceEntry{{URL: "https://example.com/before", Type: "Document", Bytes: 300, State: stateRedirect}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTestTracker()
			tracker.requests["old"] = old

			tracker.recordRequest(&tt.event)

			if tracker.sent != tt.wantSent {
				t.Errorf("sent = %d, want %d", tracker.sent, tt.wantSent)
			}
			if !maps.Equal(tracker.requests, tt.wantRequests) {
				t.Errorf("requests = %+v, want %+v", tracker.requests, tt.wantRequests)
			}
			if !slices.Equal(tracker.resources, tt.wantResources) {
				t.Errorf("resources = %+v, want %+v", tracker.resources, tt.wantResources)
			}
		})
	}
}

func TestRecordResponse(t *testing.T) {
	open := requestMeta{url: "https://example.com/", received: 7}
	done := resourceEntry{URL: "https://example.com/done", Bytes: 9, State: stateFinished}
	early := resourceEntry{Bytes: 11, State: stateFinished}
	cached := func(e resourceEntry) resourceEntry { e.FromCache = true; return e }
	tests := []struct {
		name          string
		id            network.RequestID
		fromDisk      *bool
		wantRequests  map[network.RequestID]requestMeta
		wantResources []resourceEntry
		wantEarly     map[network.RequestID]resourceEntry
	}{
		{
			name: "from disk cache", id: "open", fromDisk: new(true),
			wantRequests: map[network.RequestID]requestMeta{"open": {url: "https://example.com/", fromCache: true, received: 7}},
		},
		{name: "not from disk cache", id: "open", fromDisk: new(false)},
		{name: "no cache field", id: "open"},
		{name: "not from disk cache, unknown request", id: "unknown", fromDisk: new(false)},
		{
			name: "unknown request", id: "unknown", fromDisk: new(true),
			wantRequests: map[network.RequestID]requestMeta{"open": open, "unknown": {fromCache: true}},
		},
		{name: "skipped request", id: "skipped", fromDisk: new(true)},
		{name: "after the end", id: "done", fromDisk: new(true), wantResources: []resourceEntry{cached(done)}},
		{name: "after an end with no request", id: "early", fromDisk: new(true), wantEarly: map[network.RequestID]resourceEntry{"early": cached(early)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTestTracker()
			tracker.requests["open"] = open
			tracker.skipped["skipped"] = struct{}{}
			tracker.addEndLocked("done", done)
			tracker.early["early"] = early

			tracker.recordResponse(&network.ResponseReceivedReply{RequestID: tt.id, Response: network.Response{FromDiskCache: tt.fromDisk}})

			// A case without a want field expects that state unchanged.
			wantRequests, wantResources, wantEarly := tt.wantRequests, tt.wantResources, tt.wantEarly
			if wantRequests == nil {
				wantRequests = map[network.RequestID]requestMeta{"open": open}
			}
			if wantResources == nil {
				wantResources = []resourceEntry{done}
			}
			if wantEarly == nil {
				wantEarly = map[network.RequestID]resourceEntry{"early": early}
			}
			if !maps.Equal(tracker.requests, wantRequests) {
				t.Errorf("requests = %+v, want %+v", tracker.requests, wantRequests)
			}
			if !slices.Equal(tracker.resources, wantResources) {
				t.Errorf("resources = %+v, want %+v", tracker.resources, wantResources)
			}
			if !maps.Equal(tracker.early, wantEarly) {
				t.Errorf("early = %+v, want %+v", tracker.early, wantEarly)
			}
		})
	}
}

func TestRecordData(t *testing.T) {
	tracker := newTestTracker()
	tracker.requests["known"] = requestMeta{url: "https://example.com/a", resourceType: "Fetch", fromCache: true}

	tracker.recordData(&network.DataReceivedReply{RequestID: "known", DataLength: 9000, EncodedDataLength: 1000})
	tracker.recordData(&network.DataReceivedReply{RequestID: "known", DataLength: 9000, EncodedDataLength: 234})
	tracker.recordData(&network.DataReceivedReply{RequestID: "unknown", EncodedDataLength: 50})
	tracker.skipped["skipped"] = struct{}{}
	tracker.recordData(&network.DataReceivedReply{RequestID: "skipped", EncodedDataLength: 60})

	want := map[network.RequestID]requestMeta{
		"known":   {url: "https://example.com/a", resourceType: "Fetch", fromCache: true, received: 1234},
		"unknown": {received: 50},
	}
	if !maps.Equal(tracker.requests, want) {
		t.Errorf("requests = %+v, want %+v", tracker.requests, want)
	}
	if len(tracker.resources) != 0 {
		t.Errorf("resources = %+v, want none", tracker.resources)
	}
}

func TestRecordFinished(t *testing.T) {
	tracker := newTestTracker()
	tracker.requests["known"] = requestMeta{url: "https://example.com/a.js", resourceType: "Script", fromCache: true, received: 900}
	tracker.requests["other"] = requestMeta{url: "https://example.com/b.js"}

	tracker.recordFinished(&network.LoadingFinishedReply{RequestID: "known", EncodedDataLength: 1500})
	tracker.recordFinished(&network.LoadingFinishedReply{RequestID: "unknown", EncodedDataLength: 20})
	tracker.skipped["skipped"] = struct{}{}
	tracker.recordFinished(&network.LoadingFinishedReply{RequestID: "skipped", EncodedDataLength: 30})

	want := []resourceEntry{{URL: "https://example.com/a.js", Type: "Script", Bytes: 1500, State: stateFinished, FromCache: true}}
	if !slices.Equal(tracker.resources, want) {
		t.Errorf("resources = %+v, want %+v", tracker.resources, want)
	}
	wantRequests := map[network.RequestID]requestMeta{"other": {url: "https://example.com/b.js"}}
	if !maps.Equal(tracker.requests, wantRequests) {
		t.Errorf("requests = %+v, want %+v", tracker.requests, wantRequests)
	}
	wantEarly := map[network.RequestID]resourceEntry{"unknown": {Bytes: 20, State: stateFinished}}
	if !maps.Equal(tracker.early, wantEarly) {
		t.Errorf("early = %+v, want %+v", tracker.early, wantEarly)
	}
}

func TestRecordFailed(t *testing.T) {
	tests := []struct {
		name      string
		event     network.LoadingFailedReply
		want      []resourceEntry
		wantEarly map[network.RequestID]resourceEntry
	}{
		{
			name:  "network error",
			event: network.LoadingFailedReply{RequestID: "known", ErrorText: "net::ERR_CONNECTION_RESET"},
			want:  []resourceEntry{{URL: "https://example.com/big", Type: "Fetch", Bytes: 20008, State: stateFailed}},
		},
		{
			name:  "canceled",
			event: network.LoadingFailedReply{RequestID: "known", ErrorText: "net::ERR_ABORTED", Canceled: new(true)},
			want:  []resourceEntry{{URL: "https://example.com/big", Type: "Fetch", Bytes: 20008, State: stateFailed}},
		},
		{
			name:  "stopped by the block list",
			event: network.LoadingFailedReply{RequestID: "known", BlockedReason: network.BlockedReasonInspector},
			want:  []resourceEntry{{URL: "https://example.com/big", Type: "Fetch", Bytes: 20008, State: stateBlocked}},
		},
		{
			name:      "unknown request",
			event:     network.LoadingFailedReply{RequestID: "unknown", ErrorText: "net::ERR_FAILED"},
			wantEarly: map[network.RequestID]resourceEntry{"unknown": {Bytes: 700, State: stateFailed}},
		},
		{
			name:      "unknown request stopped by the block list",
			event:     network.LoadingFailedReply{RequestID: "unknown", BlockedReason: network.BlockedReasonInspector},
			wantEarly: map[network.RequestID]resourceEntry{"unknown": {Bytes: 700, State: stateBlocked}},
		},
		{
			name:  "skipped request",
			event: network.LoadingFailedReply{RequestID: "skipped", ErrorText: "net::ERR_FAILED"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTestTracker()
			known := requestMeta{url: "https://example.com/big", resourceType: "Fetch", received: 20008}
			tracker.requests["known"] = known
			tracker.recordData(&network.DataReceivedReply{RequestID: "unknown", EncodedDataLength: 700})
			tracker.skipped["skipped"] = struct{}{}

			tracker.recordFailed(&tt.event)

			if !slices.Equal(tracker.resources, tt.want) {
				t.Errorf("resources = %+v, want %+v", tracker.resources, tt.want)
			}
			wantRequests := map[network.RequestID]requestMeta{}
			if tt.want == nil {
				wantRequests["known"] = known
			}
			if tt.wantEarly == nil {
				wantRequests["unknown"] = requestMeta{received: 700}
			}
			if !maps.Equal(tracker.requests, wantRequests) {
				t.Errorf("requests = %+v, want %+v", tracker.requests, wantRequests)
			}
			if !maps.Equal(tracker.early, tt.wantEarly) {
				t.Errorf("early = %+v, want %+v", tracker.early, tt.wantEarly)
			}
		})
	}
}

// TestEventOrderAcrossTargets feeds the events of one request in every order
// that two targets can deliver them. Each order gives the same entries.
func TestEventOrderAcrossTargets(t *testing.T) {
	request := func(url string) func(*NetworkTracker) {
		return func(tr *NetworkTracker) {
			tr.recordRequest(&network.RequestWillBeSentReply{RequestID: "doc", Type: "Document", Request: network.Request{URL: url}})
		}
	}
	response := func(tr *NetworkTracker) {
		tr.recordResponse(&network.ResponseReceivedReply{RequestID: "doc", Response: network.Response{FromDiskCache: new(true)}})
	}
	data := func(tr *NetworkTracker) {
		tr.recordData(&network.DataReceivedReply{RequestID: "doc", EncodedDataLength: 300})
	}
	finished := func(tr *NetworkTracker) {
		tr.recordFinished(&network.LoadingFinishedReply{RequestID: "doc", EncodedDataLength: 48377})
	}
	failed := func(tr *NetworkTracker) {
		tr.recordFailed(&network.LoadingFailedReply{RequestID: "doc", ErrorText: "net::ERR_ABORTED"})
	}
	frame := "https://frame.example/doc"
	tests := []struct {
		name   string
		events []func(*NetworkTracker)
		want   []resourceEntry
	}{
		{
			name:   "request, then finish",
			events: []func(*NetworkTracker){request(frame), data, finished},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished}},
		},
		{
			name:   "finish, then request",
			events: []func(*NetworkTracker){data, finished, request(frame)},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished}},
		},
		{
			name:   "request, then failure",
			events: []func(*NetworkTracker){request(frame), data, failed},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 300, State: stateFailed}},
		},
		{
			name:   "failure, then request",
			events: []func(*NetworkTracker){data, failed, request(frame)},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 300, State: stateFailed}},
		},
		{
			name:   "data and response, then request, open at Close",
			events: []func(*NetworkTracker){data, response, request(frame)},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 300, State: stateOpen, FromCache: true}},
		},
		{
			name:   "finish with no request",
			events: []func(*NetworkTracker){data, finished},
			want:   []resourceEntry{{Bytes: 48377, State: stateFinished}},
		},
		{
			name:   "data with no request",
			events: []func(*NetworkTracker){data},
			want:   []resourceEntry{{Bytes: 300, State: stateOpen}},
		},
		{
			name:   "data after the finish",
			events: []func(*NetworkTracker){request(frame), finished, data},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished}},
		},
		{
			name:   "data after the failure",
			events: []func(*NetworkTracker){request(frame), data, failed, data},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 300, State: stateFailed}},
		},
		{
			name:   "finish, then request, then data",
			events: []func(*NetworkTracker){finished, request(frame), data},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished}},
		},
		{
			name:   "data after an end with no request",
			events: []func(*NetworkTracker){finished, data},
			want:   []resourceEntry{{Bytes: 48377, State: stateFinished}},
		},
		{
			name:   "request and finish, then response",
			events: []func(*NetworkTracker){request(frame), finished, response},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished, FromCache: true}},
		},
		{
			name:   "request and response, then finish",
			events: []func(*NetworkTracker){request(frame), response, finished},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished, FromCache: true}},
		},
		{
			name:   "finish and response, then request",
			events: []func(*NetworkTracker){finished, response, request(frame)},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished, FromCache: true}},
		},
		{
			name:   "response, then finish, then request",
			events: []func(*NetworkTracker){response, finished, request(frame)},
			want:   []resourceEntry{{URL: frame, Type: "Document", Bytes: 48377, State: stateFinished, FromCache: true}},
		},
		{
			name:   "data URL, then finish",
			events: []func(*NetworkTracker){request("data:text/html,x"), data, finished},
		},
		{
			name:   "finish, then data URL",
			events: []func(*NetworkTracker){data, finished, request("data:text/html,x")},
		},
		{
			name:   "data, then data URL",
			events: []func(*NetworkTracker){data, request("data:text/html,x")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTestTracker()

			for _, event := range tt.events {
				event(tracker)
			}
			tracker.Close()

			if !slices.Equal(tracker.resources, tt.want) {
				t.Errorf("resources = %+v, want %+v", tracker.resources, tt.want)
			}
		})
	}
}

func TestCloseRecordsOpenRequests(t *testing.T) {
	tracker := newTestTracker()
	tracker.requests["a"] = requestMeta{url: "https://example.com/a", resourceType: "Fetch", received: 100000}
	tracker.requests["b"] = requestMeta{url: "https://example.com/b", resourceType: "Script", fromCache: true}
	tracker.resources = []resourceEntry{{URL: "https://example.com/", Bytes: 5, State: stateFinished}}
	tracker.early["c"] = resourceEntry{Bytes: 48377, State: stateFinished}

	tracker.Close()
	tracker.Close()

	want := []resourceEntry{
		{Bytes: 48377, State: stateFinished},
		{URL: "https://example.com/", Bytes: 5, State: stateFinished},
		{URL: "https://example.com/a", Type: "Fetch", Bytes: 100000, State: stateOpen},
		{URL: "https://example.com/b", Type: "Script", State: stateOpen, FromCache: true},
	}
	got := slices.Clone(tracker.resources)
	slices.SortFunc(got, func(a, b resourceEntry) int { return strings.Compare(a.URL, b.URL) })
	if !slices.Equal(got, want) {
		t.Errorf("resources = %+v, want %+v", got, want)
	}
	if len(tracker.requests) != 0 || len(tracker.early) != 0 {
		t.Errorf("requests = %+v, early = %+v, want none", tracker.requests, tracker.early)
	}
}

// failingNetwork is a cdp.Network whose first event subscription fails.
type failingNetwork struct {
	cdp.Network

	err error
}

//nolint:ireturn // The fake implements a cdp.Network method, which returns the interface.
func (n failingNetwork) RequestWillBeSent(context.Context) (network.RequestWillBeSentClient, error) {
	return nil, n.err
}

func TestTrack(t *testing.T) {
	errSubscribe := errors.New("subscribe failed")
	tracker := newTestTracker()

	if err := tracker.track(t.Context(), failingNetwork{err: errSubscribe}); !errors.Is(err, errSubscribe) {
		t.Fatalf("track() error = %v, want %v", err, errSubscribe)
	}
	if len(tracker.cancels) != 1 {
		t.Fatalf("cancels = %d, want 1 for the subscribed client", len(tracker.cancels))
	}

	tracker.Close()

	if err := tracker.track(t.Context(), failingNetwork{err: errSubscribe}); !errors.Is(err, errTrackerClosed) {
		t.Errorf("track() after Close error = %v, want %v", err, errTrackerClosed)
	}
	if len(tracker.cancels) != 1 {
		t.Errorf("cancels after Close = %d, want 1", len(tracker.cancels))
	}
}

// TestCloseWaitsForTrack holds a track in progress while Close starts. Close
// must cancel its client and wait for it.
func TestCloseWaitsForTrack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tracker := newTestTracker()
		canceled := make(chan struct{})
		tracker.cancels = []context.CancelFunc{func() { close(canceled) }}
		var trackDone atomic.Bool
		tracker.wg.Go(func() {
			<-canceled
			trackDone.Store(true)
		})

		tracker.Close()

		if !trackDone.Load() {
			t.Error("Close returned before the track in progress ended")
		}
	})
}

func TestAddAttachError(t *testing.T) {
	tracker := newTestTracker()
	errAttach := errors.New("iframe target T1: dialing: gone")

	tracker.addAttachError(errAttach)

	if got := tracker.summary().AttachErrors; !slices.Equal(got, []error{errAttach}) {
		t.Errorf("AttachErrors = %v, want %v", got, []error{errAttach})
	}
}

func TestNilTracker(t *testing.T) {
	var tracker *NetworkTracker
	var out strings.Builder
	logger := logtest.New(&out)

	tracker.Close()
	tracker.LogSummary(logger)

	if out.Len() != 0 {
		t.Errorf("LogSummary on a nil tracker logged %q", out.String())
	}
}

func TestNetworkSummaryTotals(t *testing.T) {
	tracker := newTestTracker()
	tracker.resources = []resourceEntry{
		{URL: "https://example.com/vendors.js", Type: "Script", Bytes: 1500000, State: stateFinished, FromCache: true},
		{URL: "https://example.com/page.html", Type: "Document", Bytes: 50000, State: stateFinished},
		{URL: "https://example.com/start", Type: "Document", Bytes: 2000, State: stateRedirect},
		{URL: "https://example.com/big", Type: "Fetch", Bytes: 20000, State: stateFailed},
		{URL: "https://example.com/hero.png", Type: "Image", State: stateBlocked},
		{URL: "https://example.com/stream", Type: "Fetch", Bytes: 3000, State: stateOpen, FromCache: true},
	}
	tracker.sent = 4321

	summary := tracker.summary()

	if summary.Received != 1575000 {
		t.Errorf("Received = %d, want 1575000", summary.Received)
	}
	if summary.Sent != 4321 {
		t.Errorf("Sent = %d, want 4321", summary.Sent)
	}
	if summary.CacheHits != 2 {
		t.Errorf("CacheHits = %d, want 2", summary.CacheHits)
	}
}

func TestNetworkSummaryNoCacheHits(t *testing.T) {
	tracker := newTestTracker()
	tracker.resources = []resourceEntry{
		{URL: "https://example.com/page.html", Type: "Document", Bytes: 50000},
		{URL: "https://example.com/style.css", Type: "Stylesheet", Bytes: 10000},
	}

	summary := tracker.summary()

	if summary.CacheHits != 0 {
		t.Errorf("CacheHits = %d, want 0", summary.CacheHits)
	}
}

func TestNetworkSummarySortedLargestFirst(t *testing.T) {
	tracker := newTestTracker()
	tracker.resources = []resourceEntry{
		{URL: "https://example.com/small.js", Bytes: 100},
		{URL: "https://example.com/tie-b.js", Bytes: 1000},
		{URL: "https://example.com/large.js", Bytes: 10000},
		{URL: "https://example.com/tie-a.js", Bytes: 1000},
	}

	summary := tracker.summary()

	got := make([]string, len(summary.Resources))
	for i, r := range summary.Resources {
		got[i] = r.URL
	}
	want := []string{"https://example.com/large.js", "https://example.com/tie-a.js", "https://example.com/tie-b.js", "https://example.com/small.js"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestNetworkSummaryByDomainAndType(t *testing.T) {
	tracker := newTestTracker()
	tracker.resources = []resourceEntry{
		{URL: "https://example.com/a.js", Type: "Script", Bytes: 1000},
		{URL: "https://example.com/b.js", Type: "Script", Bytes: 2000},
		{URL: "https://other.com/c.html", Type: "Document", Bytes: 500},
		{URL: "https://bad host/", Type: "Document", Bytes: 7},
	}

	summary := tracker.summary()

	wantDomain := map[string]int64{"example.com": 3000, "other.com": 500}
	if !maps.Equal(summary.ByDomain, wantDomain) {
		t.Errorf("ByDomain = %v, want %v", summary.ByDomain, wantDomain)
	}
	wantType := map[string]int64{"Script": 3000, "Document": 507}
	if !maps.Equal(summary.ByType, wantType) {
		t.Errorf("ByType = %v, want %v", summary.ByType, wantType)
	}
}

func TestNetworkSummaryEmpty(t *testing.T) {
	summary := newTestTracker().summary()

	if len(summary.Resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(summary.Resources))
	}
	if summary.CacheHits != 0 {
		t.Errorf("CacheHits = %d, want 0", summary.CacheHits)
	}
}

func TestNetworkTrackerCloseIdempotent(t *testing.T) {
	var cancelCount atomic.Int32
	tracker := &NetworkTracker{
		requests: make(map[network.RequestID]requestMeta),
		cancels: []context.CancelFunc{func() {
			cancelCount.Add(1)
		}},
	}

	// Call Close multiple times — cancel should execute exactly once.
	tracker.Close()
	tracker.Close()
	tracker.Close()

	if got := cancelCount.Load(); got != 1 {
		t.Errorf("cancel called %d times, want 1", got)
	}
}

func TestNetworkTrackerCloseConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cancelCount atomic.Int32
		tracker := &NetworkTracker{
			requests: make(map[network.RequestID]requestMeta),
			cancels: []context.CancelFunc{func() {
				cancelCount.Add(1)
			}},
		}

		// Race 100 concurrent Close calls — cancel must execute exactly once.
		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() {
				tracker.Close()
			})
		}
		wg.Wait()

		if got := cancelCount.Load(); got != 1 {
			t.Errorf("cancel called %d times, want 1", got)
		}
	})
}

// logSummaryLines runs LogSummary and returns its log lines, sorted. Map order
// varies, so the lines are sorted. The rank attribute keeps the resource order.
func logSummaryLines(tracker *NetworkTracker) []string {
	var out strings.Builder
	tracker.LogSummary(logtest.New(&out))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	slices.Sort(lines)
	return lines
}

// manyResources returns n resources of distinct sizes on one domain, and the
// log lines that LogSummary writes for them.
func manyResources(n int) ([]resourceEntry, []string) {
	resources := make([]resourceEntry, n)
	for i := range n {
		resources[i] = resourceEntry{URL: fmt.Sprintf("https://example.com/%d", i), Type: "Script", Bytes: int64(i + 1), State: stateFinished}
	}
	total := n * (n + 1) / 2
	want := []string{
		fmt.Sprintf(`level=DEBUG msg="browser bandwidth breakdown" total_recv=%d total_recv_size=%dB total_sent=0 total_sent_size=0B resources=%d cache_hits=0`, total, total, n),
		fmt.Sprintf(`level=DEBUG msg="bandwidth by type" type=Script bytes=%d size=%dB`, total, total),
		fmt.Sprintf(`level=DEBUG msg="bandwidth by domain" domain=example.com bytes=%d size=%dB`, total, total),
	}
	for rank := 1; rank <= n; rank++ {
		size := n - rank + 1
		want = append(want, fmt.Sprintf(`level=DEBUG msg=resource rank=%d bytes=%d size=%dB state=finished cached=false type=Script url=https://example.com/%d`, rank, size, size, size-1))
	}
	return resources, want
}

func TestLogSummary(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("x", 200)
	eleven, elevenWant := manyResources(11)
	tests := []struct {
		name         string
		resources    []resourceEntry
		sent         int64
		attachErrors []error
		want         []string
	}{
		{
			name: "unknown type, states, cache and rank",
			resources: []resourceEntry{
				{URL: "https://other.com/b", Bytes: 100, State: stateFailed},
				{URL: "https://example.com/a.js", Type: "Script", Bytes: 2100, State: stateFinished, FromCache: true},
				{URL: "https://example.com/hero.png", Type: "Image", State: stateBlocked},
			},
			sent: 300,
			want: []string{
				`level=DEBUG msg="browser bandwidth breakdown" total_recv=2200 total_recv_size=2.1KiB total_sent=300 total_sent_size=300B resources=3 cache_hits=1`,
				`level=DEBUG msg="bandwidth by type" type=Script bytes=2100 size=2.1KiB`,
				`level=DEBUG msg="bandwidth by type" type=Image bytes=0 size=0B`,
				`level=DEBUG msg="bandwidth by type" type=unknown bytes=100 size=100B`,
				`level=DEBUG msg="bandwidth by domain" domain=example.com bytes=2100 size=2.1KiB`,
				`level=DEBUG msg="bandwidth by domain" domain=other.com bytes=100 size=100B`,
				`level=DEBUG msg=resource rank=1 bytes=2100 size=2.1KiB state=finished cached=true type=Script url=https://example.com/a.js`,
				`level=DEBUG msg=resource rank=2 bytes=100 size=100B state=failed cached=false type="" url=https://other.com/b`,
				`level=DEBUG msg=resource rank=3 bytes=0 size=0B state=blocked cached=false type=Image url=https://example.com/hero.png`,
			},
		},
		{
			name:      "URL of 120 characters",
			resources: []resourceEntry{{URL: long[:120], Type: "Script", Bytes: 10, State: stateOpen}},
			want: []string{
				`level=DEBUG msg="browser bandwidth breakdown" total_recv=10 total_recv_size=10B total_sent=0 total_sent_size=0B resources=1 cache_hits=0`,
				`level=DEBUG msg="bandwidth by type" type=Script bytes=10 size=10B`,
				`level=DEBUG msg="bandwidth by domain" domain=example.com bytes=10 size=10B`,
				`level=DEBUG msg=resource rank=1 bytes=10 size=10B state=open cached=false type=Script url=` + long[:120],
			},
		},
		{
			name:      "URL of 121 characters",
			resources: []resourceEntry{{URL: long[:121], Type: "Script", Bytes: 10, State: stateRedirect}},
			want: []string{
				`level=DEBUG msg="browser bandwidth breakdown" total_recv=10 total_recv_size=10B total_sent=0 total_sent_size=0B resources=1 cache_hits=0`,
				`level=DEBUG msg="bandwidth by type" type=Script bytes=10 size=10B`,
				`level=DEBUG msg="bandwidth by domain" domain=example.com bytes=10 size=10B`,
				`level=DEBUG msg=resource rank=1 bytes=10 size=10B state=redirect cached=false type=Script url=` + long[:120] + "...",
			},
		},
		{name: "every resource of 11", resources: eleven, want: elevenWant},
		{
			name:         "attach errors and a resource with no URL",
			resources:    []resourceEntry{{Bytes: 20, State: stateFinished}},
			attachErrors: []error{errors.New("iframe target T1: dialing: gone"), errors.New("worker target T2: resuming: closed")},
			want: []string{
				`level=DEBUG msg="browser bandwidth breakdown" total_recv=20 total_recv_size=20B total_sent=0 total_sent_size=0B resources=1 cache_hits=0`,
				`level=DEBUG msg="bandwidth by type" type=unknown bytes=20 size=20B`,
				`level=DEBUG msg="bandwidth by domain" domain="" bytes=20 size=20B`,
				`level=DEBUG msg="child target attach failed" error="iframe target T1: dialing: gone"`,
				`level=DEBUG msg="child target attach failed" error="worker target T2: resuming: closed"`,
				`level=DEBUG msg=resource rank=1 bytes=20 size=20B state=finished cached=false type="" url=""`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTestTracker()
			tracker.resources = tt.resources
			tracker.sent = tt.sent
			tracker.attachErrors = tt.attachErrors

			got := logSummaryLines(tracker)

			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("log lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
