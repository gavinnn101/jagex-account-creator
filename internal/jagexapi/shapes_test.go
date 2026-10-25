package jagexapi

import (
	"context"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sardanioss/httpcloak/client"
)

// chromeShape is a request shape as Chrome ShapeChromeMajor sends it. The orders and values
// below are copied from the Chrome captures. A shape edit without a new capture
// fails the tests.
type chromeShape struct {
	order  []string
	values map[string]string
}

// with returns s with the value of name replaced.
func (s chromeShape) with(name, value string) chromeShape {
	s.values = maps.Clone(s.values)
	s.values[name] = value
	return s
}

// chromeXHRValues returns the values of a Chrome XHR, with fixed added.
func chromeXHRValues(fixed map[string]string) map[string]string {
	v := map[string]string{
		"sec-fetch-site":  "same-origin",
		"sec-fetch-mode":  "cors",
		"sec-fetch-dest":  "empty",
		"accept-encoding": "gzip, deflate, br, zstd",
		"accept-language": "en-US,en;q=0.9",
		"priority":        "u=1, i",
	}
	maps.Copy(v, fixed)
	return v
}

// chromeNavigationValues returns the values of a Chrome page load.
func chromeNavigationValues(clicked bool) map[string]string {
	v := map[string]string{
		"upgrade-insecure-requests": "1",
		"accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/jxl,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"sec-fetch-site":            "same-origin",
		"sec-fetch-mode":            "navigate",
		"sec-fetch-dest":            "document",
		"accept-encoding":           "gzip, deflate, br, zstd",
		"accept-language":           "en-US,en;q=0.9",
		"priority":                  "u=0, i",
	}
	if clicked {
		v["sec-fetch-user"] = "?1"
	}
	return v
}

func chromeLoginLink() chromeShape {
	return chromeShape{
		order: []string{
			"sec-ch-ua-platform", "user-agent", "accept", "sec-ch-ua", "locale", "sec-ch-ua-mobile",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: chromeXHRValues(map[string]string{"accept": "application/json, text/plain, */*", "locale": "en-GB"}),
	}
}

// chromeNavigation holds the sec-fetch-user slot of a clicked page load. An
// unclicked page load sends the same order without it.
func chromeNavigation(clicked bool) chromeShape {
	return chromeShape{
		order: []string{
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-full-version", "sec-ch-ua-arch", "sec-ch-ua-platform",
			"sec-ch-ua-platform-version", "sec-ch-ua-model", "sec-ch-ua-bitness", "sec-ch-ua-full-version-list",
			"upgrade-insecure-requests", "user-agent", "accept",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: chromeNavigationValues(clicked),
	}
}

func chromeRedirect(clicked bool) chromeShape {
	return chromeShape{
		order: []string{
			"upgrade-insecure-requests", "user-agent", "accept",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest",
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-full-version", "sec-ch-ua-arch", "sec-ch-ua-platform",
			"sec-ch-ua-platform-version", "sec-ch-ua-model", "sec-ch-ua-bitness", "sec-ch-ua-full-version-list",
			"referer", "accept-encoding", "accept-language", "cookie", "priority",
		},
		values: chromeNavigationValues(clicked),
	}
}

// chromeXHRPost is a POST with no locale, as the registration init sends it.
func chromeXHRPost() chromeShape {
	return chromeShape{
		order: []string{
			"content-length", "sec-ch-ua-full-version-list", "sec-ch-ua-platform", "sec-ch-ua", "sec-ch-ua-bitness",
			"sec-ch-ua-model", "sec-ch-ua-mobile", "sec-ch-ua-arch", "sec-ch-ua-full-version",
			"accept", "content-type", "user-agent", "sec-ch-ua-platform-version", "origin",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: chromeXHRValues(map[string]string{
			"accept":       "application/json, text/plain, */*",
			"content-type": "application/json",
			"origin":       "https://account.jagex.com",
		}),
	}
}

// chromeCurrentGet is a GET of /current. The if-none-match slot is from a GET after a full answer.
func chromeCurrentGet() chromeShape {
	return chromeShape{
		order: []string{
			"sec-ch-ua-full-version-list", "sec-ch-ua-platform", "sec-ch-ua", "sec-ch-ua-bitness",
			"sec-ch-ua-model", "sec-ch-ua-mobile", "sec-ch-ua-arch", "sec-ch-ua-full-version",
			"accept", "locale", "user-agent", "sec-ch-ua-platform-version",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "if-none-match", "priority",
		},
		values: chromeXHRValues(map[string]string{"accept": "application/json, text/plain, */*", "locale": "en-GB"}),
	}
}

// chromeLocaleXHRPost is a login SPA POST after the init, or a manage SPA POST.
func chromeLocaleXHRPost() chromeShape {
	return chromeShape{
		order: []string{
			"content-length", "sec-ch-ua-full-version-list", "sec-ch-ua-platform", "sec-ch-ua", "sec-ch-ua-bitness",
			"sec-ch-ua-model", "sec-ch-ua-mobile", "sec-ch-ua-arch", "sec-ch-ua-full-version",
			"accept", "content-type", "locale", "user-agent", "sec-ch-ua-platform-version", "origin",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: chromeXHRValues(map[string]string{
			"accept":       "application/json, text/plain, */*",
			"content-type": "application/json",
			"locale":       "en-GB",
			"origin":       "https://account.jagex.com",
		}),
	}
}

func TestShapes_MatchChrome(t *testing.T) {
	tests := []struct {
		name string
		got  requestShape
		want chromeShape
	}{
		{"login link", loginLinkShape(), chromeLoginLink()},
		{"navigation", navigationShape(false), chromeNavigation(false)},
		{"clicked navigation", navigationShape(true), chromeNavigation(true)},
		{"redirect", redirectShape(false), chromeRedirect(false)},
		{"clicked redirect", redirectShape(true), chromeRedirect(true)},
		{"XHR POST", xhrPostShape(), chromeXHRPost()},
		{"current GET", currentGetShape(), chromeCurrentGet()},
		{"XHR POST with locale", localeXHRPostShape(), chromeLocaleXHRPost()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got.order, tt.want.order) {
				t.Errorf("order = %v, want %v", tt.got.order, tt.want.order)
			}
			if !maps.Equal(tt.got.values, tt.want.values) {
				t.Errorf("values = %v, want %v", tt.got.values, tt.want.values)
			}
			for name := range tt.got.values {
				if !slices.Contains(tt.got.order, name) {
					t.Errorf("value %q has no slot in the order", name)
				}
			}
		})
	}
}

// clientHeaderValues returns the values that the test client sends: the user agent and testHints.
func clientHeaderValues() map[string]string {
	h := testHints()
	return map[string]string{
		"user-agent":                  "test",
		"sec-ch-ua":                   h.SecChUA,
		"sec-ch-ua-mobile":            h.SecChUAMobile,
		"sec-ch-ua-platform":          h.SecChUAPlatform,
		"sec-ch-ua-arch":              h.SecChUAArch,
		"sec-ch-ua-bitness":           h.SecChUABitness,
		"sec-ch-ua-full-version":      h.SecChUAFullVersion,
		"sec-ch-ua-full-version-list": h.SecChUAFullVersionList,
		"sec-ch-ua-model":             h.SecChUAModel,
		"sec-ch-ua-platform-version":  h.SecChUAPlatformVersion,
	}
}

// assertRequest fails t unless req is a request of shape want for wantURL with
// the referer wantReferer. The request must send each header of the order that
// has a value, with that value, and no other header.
func assertRequest(t *testing.T, label string, req *client.Request, method, wantURL, wantReferer string, want chromeShape) {
	t.Helper()
	if req.Method != method || req.URL != wantURL {
		t.Errorf("%s = %s %s, want %s %s", label, req.Method, req.URL, method, wantURL)
	}
	if req.Referer != "" {
		t.Errorf("%s Referer field = %q, want empty: TLS-only mode does not send it", label, req.Referer)
	}
	if (req.Body == nil) != (method == "GET") {
		t.Errorf("%s body = %v, want a body only on a POST", label, req.Body)
	}
	if !slices.Equal(req.HeaderOrder, want.order) {
		t.Errorf("%s header order = %v, want %v", label, req.HeaderOrder, want.order)
	}
	known := clientHeaderValues()
	maps.Copy(known, want.values)
	known["referer"] = wantReferer
	wantHeaders := map[string]string{}
	for _, name := range want.order {
		if value, ok := known[name]; ok {
			wantHeaders[name] = value
		}
	}
	gotHeaders := make(map[string]string, len(req.Headers))
	for name, values := range req.Headers {
		gotHeaders[name] = strings.Join(values, "\x00")
	}
	if !maps.Equal(gotHeaders, wantHeaders) {
		t.Errorf("%s headers = %v, want %v", label, gotHeaders, wantHeaders)
	}
}

func TestNewRequest(t *testing.T) {
	rc := newTestRC(newTestClient(t))
	shape := requestShape{
		order:  []string{"cookie", "sec-ch-ua", "accept", "user-agent", "referer", "content-length"},
		values: map[string]string{"accept": "shape", "user-agent": "shape", "origin": "no slot"},
	}

	get := rc.newRequest(shape, "GET", "https://account.jagex.com/a", nil, map[string]string{"accept": "own", "referer": "r"})
	want := map[string][]string{"sec-ch-ua": {testHints().SecChUA}, "accept": {"own"}, "user-agent": {"shape"}, "referer": {"r"}}
	if !maps.EqualFunc(get.Headers, want, slices.Equal) {
		t.Errorf("headers = %v, want %v", get.Headers, want)
	}
	if get.Method != "GET" || get.URL != "https://account.jagex.com/a" || get.Body != nil || !slices.Equal(get.HeaderOrder, shape.order) {
		t.Errorf("request = %s %s body %v order %v, want GET with no body in the shape's order", get.Method, get.URL, get.Body, get.HeaderOrder)
	}

	post := rc.newRequest(shape, "POST", "https://account.jagex.com/b", []byte(`{"k":"v"}`), nil)
	body, err := io.ReadAll(post.Body)
	if err != nil || string(body) != `{"k":"v"}` {
		t.Errorf("body = %q, %v, want the given bytes", body, err)
	}
	if accept := post.Headers["accept"]; !slices.Equal(accept, []string{"shape"}) {
		t.Errorf("accept with no own value = %v, want the shape's", accept)
	}
}

func TestPostJSON_MarshalError(t *testing.T) {
	rc := newTestRCWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		t.Errorf("sent %s %s, want no request", req.Method, req.URL)
		return mockResp(200, `{}`), nil
	})
	// A channel does not marshal to JSON.
	_, err := rc.postJSON(context.Background(), xhrPostShape(), registrationBaseURL, testLandingURL, map[string]any{"bad": make(chan int)}, "")
	if err == nil || !strings.HasPrefix(err.Error(), "marshalling request body: ") {
		t.Errorf("error = %v, want a marshalling error", err)
	}
}

// builtRequests runs every call of the HTTP phase on a scripted transport and returns the requests that it sent.
func builtRequests(t *testing.T) []*client.Request {
	t.Helper()
	var all []*client.Request

	c, got := newScriptedClient(t, []*client.Response{mockResp(200, `{"loginLink":"`+testLoginLink+`"}`)})
	if _, err := LoginLink(context.Background(), c, c.logger); err != nil {
		t.Fatalf("LoginLink: %v", err)
	}
	all = append(all, *got...)

	c, got = newScriptedClient(t, startAnswers())
	if _, err := StartRegistration(context.Background(), c, c.logger, loginLinkURL(t)); err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	all = append(all, *got...)

	for _, call := range registrationCalls() {
		rc, got := call.scriptedRC(t, 200)
		if err := call.call(t, t.Context(), rc); err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		all = append(all, *got...)
	}

	rc, got := newScriptedRC(t, loginAnswers())
	if err := rc.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	return append(all, *got...)
}

func TestHeaderOrders_ListEverySentHeader(t *testing.T) {
	// The transport adds cookie and content-length in their slots. Only a clicked page load sends sec-fetch-user.
	// Only a GET of /current after a full answer sends if-none-match.
	unsent := []string{"cookie", "content-length", "sec-fetch-user", "if-none-match"}
	reqs := builtRequests(t)
	if len(reqs) != 19 {
		t.Fatalf("requests = %d, want the 19 of the HTTP phase", len(reqs))
	}
	for i, req := range reqs {
		if req.Referer != "" {
			t.Errorf("request %d (%s) Referer field = %q, want empty", i, req.URL, req.Referer)
		}
		slots := make(map[string]bool, len(req.HeaderOrder))
		for _, name := range req.HeaderOrder {
			if slots[name] {
				t.Errorf("request %d (%s) order lists %q twice", i, req.URL, name)
			}
			slots[name] = true
			if _, ok := req.Headers[name]; !ok && !slices.Contains(unsent, name) {
				t.Errorf("request %d (%s) sends no %q", i, req.URL, name)
			}
		}
		for name := range req.Headers {
			if !slots[name] {
				t.Errorf("request %d (%s) order has no slot for %q", i, req.URL, name)
			}
		}
	}
}
