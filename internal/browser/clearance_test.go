package browser

import (
	"bytes"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mafredri/cdp/protocol/network"

	"jagex_account_creator/internal/logtest"
)

const (
	testJSDURL       = "https://example.com/cdn-cgi/challenge-platform/h/g/jsd/oneshot/abc/0.1:1:x/def"
	testChallengeURL = "https://example.com/cdn-cgi/challenge-platform/h/g/fo/abc/def"
)

func TestClassifyClearance(t *testing.T) {
	const clearance = `cf_clearance=v1; path=/; domain=.example.com; HttpOnly; Secure`
	tests := []struct {
		name    string
		url     string
		headers string
		want    clearanceSource
	}{
		{"one cookie from a JSD URL", testJSDURL, `{"set-cookie":"` + clearance + `"}`, sourceJSD},
		{"one cookie from a fo URL", testChallengeURL, `{"set-cookie":"` + clearance + `"}`, sourceChallenge},
		{"one cookie from an unknown URL", "", `{"set-cookie":"` + clearance + `"}`, sourceChallenge},
		{"two cookies joined", testJSDURL, `{"set-cookie":"__cf_bm=b; path=/\n ` + clearance + `"}`, sourceJSD},
		{"mixed-case key", testChallengeURL, `{"Set-Cookie":"` + clearance + `"}`, sourceChallenge},
		{"another cookie only", testJSDURL, `{"set-cookie":"__cf_bm=b; path=/\n_cfuvid=c"}`, sourceNone},
		{"the name inside a value", testChallengeURL, `{"set-cookie":"x=cf_clearance=v1"}`, sourceNone},
		{"no Set-Cookie", testJSDURL, `{"content-type":"text/html","x":"cf_clearance=v1"}`, sourceNone},
		{"headers do not decode", testJSDURL, `not json`, sourceNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyClearance(tt.url, network.Headers(tt.headers)); got != tt.want {
				t.Errorf("classifyClearance = %q, want %q", got, tt.want)
			}
		})
	}
}

// clearanceEvents returns the request and the response of one cf_clearance that url sets.
func clearanceEvents(id, url string) (*network.RequestWillBeSentReply, *network.ResponseReceivedExtraInfoReply) {
	request := &network.RequestWillBeSentReply{RequestID: network.RequestID(id), Request: network.Request{URL: url}}
	response := &network.ResponseReceivedExtraInfoReply{
		RequestID: network.RequestID(id),
		Headers:   network.Headers(`{"set-cookie":"cf_clearance=v; path=/"}`),
	}
	return request, response
}

// isClosed reports whether done is closed, without blocking.
func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func TestClearanceGate(t *testing.T) {
	tests := []struct {
		name  string
		order []string // the URL of each cf_clearance, in the order that it is set
		want  []bool   // done is closed after each one
	}{
		{"JSD first", []string{testJSDURL, testChallengeURL}, []bool{false, true}},
		{"challenge first", []string{testChallengeURL, testJSDURL}, []bool{false, true}},
		{"challenge twice", []string{testChallengeURL, testChallengeURL}, []bool{false, false}},
		{"JSD twice", []string{testJSDURL, testJSDURL}, []bool{false, false}},
		// A repeat after the close must not close done again.
		{"challenge after both", []string{testJSDURL, testChallengeURL, testChallengeURL}, []bool{false, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			g := newClearanceGate(nil, logtest.New(&buf), "Security | Example", func() {})

			for i, url := range tt.order {
				request, response := clearanceEvents(string(rune('a'+i)), url)
				g.recordRequest(request)
				g.recordResponse(response)
				if got := isClosed(g.done); got != tt.want[i] {
					t.Errorf("done closed after %d = %v, want %v", i+1, got, tt.want[i])
				}
			}

			// Each source logs once, however often it sets the cookie.
			lines := strings.Count(buf.String(), `msg="cf_clearance source seen"`)
			wantLines := 1
			if tt.want[len(tt.want)-1] {
				wantLines = 2
			}
			if lines != wantLines {
				t.Errorf("source lines = %d, want %d:\n%s", lines, wantLines, buf.String())
			}
			first := sourceChallenge
			if tt.order[0] == testJSDURL {
				first = sourceJSD
			}
			if !strings.Contains(buf.String(), `msg="cf_clearance source seen" source=`+string(first)+` since_start=`) {
				t.Errorf("log has no line for the first source:\n%s", buf.String())
			}
		})
	}
}

func TestClearanceGateIgnoresOtherResponses(t *testing.T) {
	var buf bytes.Buffer
	g := newClearanceGate(nil, logtest.New(&buf), "Security | Example", func() {})

	g.recordRequest(&network.RequestWillBeSentReply{RequestID: "a", Request: network.Request{URL: testJSDURL}})
	g.recordResponse(&network.ResponseReceivedExtraInfoReply{RequestID: "a", Headers: network.Headers(`{"set-cookie":"__cf_bm=b"}`)})

	if len(g.seen) != 0 || buf.Len() != 0 {
		t.Errorf("seen = %v, log %q, want nothing", g.seen, buf.String())
	}
}

func TestClearanceGateKeepsTheLastHop(t *testing.T) {
	g := newClearanceGate(nil, slog.New(slog.DiscardHandler), "Security | Example", func() {})
	request, response := clearanceEvents("a", testChallengeURL)
	g.recordRequest(&network.RequestWillBeSentReply{RequestID: "a", Request: network.Request{URL: testJSDURL}})
	g.recordRequest(request)
	g.recordResponse(response)

	if !g.seen[sourceChallenge] || g.seen[sourceJSD] {
		t.Errorf("seen = %v, want the challenge only", g.seen)
	}
}

func TestClearanceGateCloseIdempotent(t *testing.T) {
	var cancels atomic.Int32
	g := newClearanceGate(nil, slog.New(slog.DiscardHandler), "Security | Example", func() { cancels.Add(1) })

	g.Close()
	g.Close()

	if got := cancels.Load(); got != 1 {
		t.Errorf("cancel called %d times, want 1", got)
	}
}
