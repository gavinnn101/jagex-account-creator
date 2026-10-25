package jagexapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"testing"

	"github.com/sardanioss/httpcloak/client"
	"github.com/sardanioss/httpcloak/fingerprint"
)

func TestNewClient_HeaderValues(t *testing.T) {
	c := newTestClient(t)

	if want := clientHeaderValues(); !maps.Equal(c.values, want) {
		t.Errorf("values = %v, want %v", c.values, want)
	}
}

func TestNewClient_LogsEveryHint(t *testing.T) {
	var buf bytes.Buffer
	hints := testHints()
	c := NewClient(debugLogger(&buf), "test", hints, "")
	defer c.Close()

	want := map[string]any{
		"sec_ch_ua":                   hints.SecChUA,
		"sec_ch_ua_mobile":            hints.SecChUAMobile,
		"sec_ch_ua_platform":          hints.SecChUAPlatform,
		"sec_ch_ua_arch":              hints.SecChUAArch,
		"sec_ch_ua_bitness":           hints.SecChUABitness,
		"sec_ch_ua_full_version":      hints.SecChUAFullVersion,
		"sec_ch_ua_full_version_list": hints.SecChUAFullVersionList,
		"sec_ch_ua_model":             hints.SecChUAModel,
		"sec_ch_ua_platform_version":  hints.SecChUAPlatformVersion,
	}
	for _, rec := range debugRecords(t, &buf) {
		if rec["msg"] != "client hints set" {
			continue
		}
		for key, value := range want {
			if rec[key] != value {
				t.Errorf("%s = %v, want %q", key, rec[key], value)
			}
		}
		return
	}
	t.Errorf("no client hints line in:\n%s", buf.String())
}

func TestImportCookies_SecondImportReplacesSameName(t *testing.T) {
	c := newTestClient(t)

	// The run imports the early cookies, then every cookie at the clearance gate, into one jar.
	c.ImportCookies([]client.Cookie{
		{Name: "__cf_bm", Value: "v1", Domain: ".jagex.com", Path: "/", Secure: true},
	})
	c.ImportCookies([]client.Cookie{
		{Name: "__cf_bm", Value: "v2", Domain: ".jagex.com", Path: "/", Secure: true},
		{Name: "cf_clearance", Value: "c1", Domain: ".jagex.com", Path: "/", Secure: true},
	})

	got := map[string][]string{}
	for _, ck := range jarCookies(t, c, "https://account.jagex.com/") {
		got[ck.Name] = append(got[ck.Name], ck.Value)
	}
	want := map[string][]string{"__cf_bm": {"v2"}, "cf_clearance": {"c1"}}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("jar sends %v, want %v", got, want)
	}
}

// --- ImportCookies → cookie jar round-trip tests ---

// jarCookies returns the cookies the jar would send for rawURL.
func jarCookies(t *testing.T, c *Client, rawURL string) []*client.Cookie {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %q: %v", rawURL, err)
	}
	return c.inner.Cookies().Cookies(u)
}

func TestImportCookies_RoundTrip_Basic(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{Name: "session", Value: "abc123", Domain: ".example.com", Path: "/", Secure: true},
		{Name: "pref", Value: "dark", Domain: ".example.com", Path: "/"},
	}
	c.ImportCookies(cookies)

	got := jarCookies(t, c, "https://example.com/page")
	if len(got) != 2 {
		t.Fatalf("got %d cookies, want 2", len(got))
	}

	names := map[string]string{}
	for _, ck := range got {
		names[ck.Name] = ck.Value
	}
	if names["session"] != "abc123" {
		t.Errorf("session cookie value = %q, want %q", names["session"], "abc123")
	}
	if names["pref"] != "dark" {
		t.Errorf("pref cookie value = %q, want %q", names["pref"], "dark")
	}
}

func TestImportCookies_RoundTrip_LeadingDotDomain(t *testing.T) {
	c := newTestClient(t)

	// Cookies with leading dot domain (as CDP returns them) should be
	// retrievable for both the bare domain and subdomains.
	cookies := []client.Cookie{
		{Name: "cf_clearance", Value: "token123", Domain: ".jagex.com", Path: "/", Secure: true},
	}
	c.ImportCookies(cookies)

	// Should work for bare domain.
	got := jarCookies(t, c, "https://jagex.com/")
	if len(got) == 0 {
		t.Fatal("expected cookie for bare domain, got none")
	}
	if got[0].Value != "token123" {
		t.Errorf("value = %q, want %q", got[0].Value, "token123")
	}

	// Should also work for subdomains.
	got = jarCookies(t, c, "https://account.jagex.com/api")
	if len(got) == 0 {
		t.Fatal("expected cookie for subdomain, got none")
	}
}

func TestImportCookies_RoundTrip_MultiDomain(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{Name: "a", Value: "1", Domain: ".jagex.com", Path: "/", Secure: true},
		{Name: "b", Value: "2", Domain: ".cloudflare.com", Path: "/", Secure: true},
		{Name: "c", Value: "3", Domain: ".jagex.com", Path: "/", Secure: true},
	}
	c.ImportCookies(cookies)

	// Jagex domain should get 2 cookies.
	jagex := jarCookies(t, c, "https://jagex.com/")
	if len(jagex) != 2 {
		t.Fatalf("jagex: got %d cookies, want 2", len(jagex))
	}

	// Cloudflare domain should get 1 cookie.
	cf := jarCookies(t, c, "https://cloudflare.com/")
	if len(cf) != 1 {
		t.Fatalf("cloudflare: got %d cookies, want 1", len(cf))
	}
	if cf[0].Name != "b" {
		t.Errorf("cloudflare cookie name = %q, want %q", cf[0].Name, "b")
	}

	// Unrelated domain should get 0 cookies.
	other := jarCookies(t, c, "https://example.org/")
	if len(other) != 0 {
		t.Errorf("example.org: got %d cookies, want 0", len(other))
	}
}

func TestImportCookies_EmptyDomainSkipped(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{Name: "valid", Value: "yes", Domain: ".example.com", Path: "/", Secure: true},
		{Name: "empty", Value: "skip", Domain: "", Path: "/"},
	}
	c.ImportCookies(cookies)

	got := jarCookies(t, c, "https://example.com/")
	if len(got) != 1 {
		t.Fatalf("got %d cookies, want 1 (empty domain should be skipped)", len(got))
	}
	if got[0].Name != "valid" {
		t.Errorf("cookie name = %q, want %q", got[0].Name, "valid")
	}
	// No request URL finds a cookie stored under the key "", so only the count shows the skip.
	if n := c.inner.Cookies().Count(); n != 1 {
		t.Errorf("jar holds %d cookies, want 1", n)
	}
}

func TestImportCookies_EmptySlice(t *testing.T) {
	c := newTestClient(t)

	c.ImportCookies([]client.Cookie{})

	if got := jarCookies(t, c, "https://example.com/"); len(got) != 0 {
		t.Errorf("got %d cookies, want 0 after empty import", len(got))
	}
}

func TestImportCookies_NilSlice(t *testing.T) {
	c := newTestClient(t)

	c.ImportCookies(nil)

	if got := jarCookies(t, c, "https://example.com/"); len(got) != 0 {
		t.Errorf("got %d cookies, want 0 after nil import", len(got))
	}
}

func TestImportCookies_SecureCookieNotReturnedForHTTP(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{Name: "secure_only", Value: "secret", Domain: ".example.com", Path: "/", Secure: true},
	}
	c.ImportCookies(cookies)

	// Secure cookie should NOT be returned for http:// URL.
	if got := jarCookies(t, c, "http://example.com/"); len(got) != 0 {
		t.Errorf("secure cookie returned for http:// URL, got %d cookies", len(got))
	}

	// But should be returned for https://.
	if got := jarCookies(t, c, "https://example.com/"); len(got) != 1 {
		t.Errorf("secure cookie NOT returned for https:// URL, got %d cookies", len(got))
	}
}

func TestImportCookies_PathScoping(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{Name: "root", Value: "r", Domain: ".example.com", Path: "/", Secure: true},
		{Name: "api", Value: "a", Domain: ".example.com", Path: "/api", Secure: true},
	}
	c.ImportCookies(cookies)

	// Root path should only get the root cookie.
	got := jarCookies(t, c, "https://example.com/")
	if len(got) != 1 {
		t.Fatalf("root path: got %d cookies, want 1", len(got))
	}
	if got[0].Name != "root" {
		t.Errorf("root path cookie = %q, want %q", got[0].Name, "root")
	}

	// /api path should get both cookies.
	got = jarCookies(t, c, "https://example.com/api/endpoint")
	if len(got) != 2 {
		t.Fatalf("/api path: got %d cookies, want 2", len(got))
	}
}

// allJarCookies returns every cookie in the jar, whatever its domain and path.
func allJarCookies(c *Client) []*client.Cookie {
	var out []*client.Cookie
	for _, cookies := range c.inner.Cookies().AllCookies() {
		out = append(out, cookies...)
	}
	return out
}

func TestImportCookies_AllDomains(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{Name: "cf_clearance", Value: "tok1", Domain: ".jagex.com", Path: "/", Secure: true},
		{Name: "session", Value: "tok2", Domain: ".account.jagex.com", Path: "/", Secure: true},
		{Name: "other", Value: "tok3", Domain: ".cloudflare.com", Path: "/", Secure: true},
	}
	c.ImportCookies(cookies)

	all := allJarCookies(c)
	if len(all) != 3 {
		t.Fatalf("jar holds %d cookies, want 3", len(all))
	}

	names := map[string]bool{}
	for _, ck := range all {
		names[ck.Name] = true
	}
	for _, want := range []string{"cf_clearance", "session", "other"} {
		if !names[want] {
			t.Errorf("jar has no cookie %q", want)
		}
	}
}

func TestImportCookies_StoresPathScopedCookies(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{Name: "root", Value: "r", Domain: ".example.com", Path: "/", Secure: true},
		{Name: "api", Value: "a", Domain: ".example.com", Path: "/api", Secure: true},
	}
	c.ImportCookies(cookies)

	// The jar listing ignores path scoping, so it holds both.
	all := allJarCookies(c)
	if len(all) != 2 {
		t.Fatalf("jar holds %d cookies, want 2", len(all))
	}
}

func TestImportCookies_PreservesAttributes(t *testing.T) {
	c := newTestClient(t)

	cookies := []client.Cookie{
		{
			Name:     "full",
			Value:    "val",
			Domain:   ".example.com",
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
			SameSite: "None",
		},
	}
	c.ImportCookies(cookies)

	all := allJarCookies(c)
	if len(all) != 1 {
		t.Fatalf("got %d cookies, want 1", len(all))
	}
	ck := all[0]
	if ck.Name != "full" || ck.Value != "val" {
		t.Errorf("Name/Value = %q/%q, want full/val", ck.Name, ck.Value)
	}
	if !ck.Secure {
		t.Error("Secure = false, want true")
	}
	if !ck.HttpOnly {
		t.Error("HttpOnly = false, want true")
	}
	if ck.SameSite != "None" {
		t.Errorf("SameSite = %q, want %q", ck.SameSite, "None")
	}
}

// --- DebugLogCookies guards ---

func TestDebugLogCookies_InvalidURL(t *testing.T) {
	// A target that does not parse gives no URL, so the jar lookup must not run.
	c := newTestClient(t)

	c.DebugLogCookies("%zz")
}

func TestDebugLogCookies_TargetLine(t *testing.T) {
	for _, tt := range []struct {
		name, target string
		wantLine     bool
	}{
		{"target", "https://account.jagex.com/", true},
		{"no target", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := NewClient(debugLogger(&buf), "test", testHints(), "")
			defer c.Close()
			c.ImportCookies([]client.Cookie{
				{Name: "cf_clearance", Value: "c1", Domain: ".jagex.com", Path: "/", Secure: true},
			})
			buf.Reset()

			c.DebugLogCookies(tt.target)

			var lines []map[string]any
			for _, rec := range debugRecords(t, &buf) {
				if rec["msg"] == "cookies that would be sent" {
					lines = append(lines, rec)
				}
			}
			if !tt.wantLine {
				if len(lines) != 0 {
					t.Errorf("got a target line with no target: %v", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("got %d target lines, want 1, in:\n%s", len(lines), buf.String())
			}
			rec := lines[0]
			if rec["url"] != tt.target || rec["count"] != float64(1) || fmt.Sprint(rec["names"]) != "[cf_clearance]" {
				t.Errorf("target line = %v, want url %s, count 1, names [cf_clearance]", rec, tt.target)
			}
		})
	}
}

// --- request timeout ---

func TestNewClient_RequestTimeout(t *testing.T) {
	// The server closes each connection at once, so the request fails in the
	// handshake. A zero timeout fails earlier, with a deadline error.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	c := newTestClient(t)

	_, err = c.Do(t.Context(), &client.Request{Method: "GET", URL: "https://" + listener.Addr().String() + "/"})
	if err == nil {
		t.Fatal("expected an error from a server that closes the connection")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want a handshake error, not an expired request timeout", err)
	}
}

// --- fingerprint preset pin ---

func TestPresetName_Resolves(t *testing.T) {
	if fingerprint.GetStrict(presetName()) == nil {
		t.Fatalf("GetStrict(%q) = nil; httpcloak has no preset for ShapeChromeMajor, or it renamed the preset", presetName())
	}
}
