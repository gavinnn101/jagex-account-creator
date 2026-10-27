package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/store"
)

// proxiesT0 returns the start of the first seeded attempt.
func proxiesT0() time.Time { return time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC) }

// seedProxies imports proxy 1, an HTTP proxy with credentials, and proxy 2, a
// SOCKS5 proxy with none. Proxy 1 has a success and then a failure.
func seedProxies(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := s.ImportProxies(t.Context(), "a.example:1:user1:secret1\nsocks5://b.example:2\n"); err != nil {
		t.Fatal(err)
	}
	mustWriteRun(t, s, "serve", proxiesT0(), time.Time{}, "", json.RawMessage("{}"))
	for _, a := range []store.Attempt{
		{RunID: 1, StartedAt: proxiesT0(), Proxy: "a.example:1", BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4},
		{RunID: 1, StartedAt: proxiesT0().Add(time.Minute), Proxy: "a.example:1", Category: "proxy_error", Error: "failed", BrowserBytesSent: 10},
	} {
		if err := s.WriteAttempt(t.Context(), &a, ""); err != nil {
			t.Fatal(err)
		}
	}
}

// wantProxies returns the list items of the seeded proxies. The added_at of each comes from the store.
func wantProxies(t *testing.T, s *store.Store) []api.Proxy {
	t.Helper()
	stored, err := s.Proxies(t.Context())
	if err != nil || len(stored) != 2 {
		t.Fatalf("Proxies = %+v, %v; want the two seeded proxies", stored, err)
	}
	return []api.Proxy{
		{
			Id: 1, Proxy: "a.example:1", Auth: true, Enabled: true, AddedAt: stored[0].AddedAt, Attempts: 2, Successes: 1,
			BrowserBytesSent: 11, BrowserBytesReceived: 2, HttpBytesSent: 3, HttpBytesReceived: 4,
			LastAttemptAt: new(proxiesT0().Add(time.Minute)), LastFailure: new("proxy_error"),
		},
		{Id: 2, Proxy: "socks5://b.example:2", Enabled: true, AddedAt: stored[1].AddedAt},
	}
}

// listProxies returns the list answer of h.
func listProxies(t *testing.T, h http.Handler) api.ProxyList {
	t.Helper()
	var got api.ProxyList
	decodeBody(t, doAPI(t.Context(), h, "GET", "/api/proxies", ""), http.StatusOK, &got)
	return got
}

// assertNoSecret fails t when body holds a credential of the seeded proxies.
func assertNoSecret(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"user1", "secret1"} {
		if strings.Contains(body, secret) {
			t.Errorf("the answer holds %s:\n%s", secret, body)
		}
	}
}

func TestListProxies(t *testing.T) {
	s := newHistoryAPI(t)
	t.Run("no proxy", func(t *testing.T) {
		if w := doAPI(t.Context(), s.handler(), "GET", "/api/proxies", ""); w.Code != http.StatusOK || w.Body.String() != "{\"proxies\":[]}\n" {
			t.Errorf("answer = %d %s, want 200 with an empty list", w.Code, w.Body.String())
		}
	})
	seedProxies(t, s.store)
	t.Run("each proxy with its health and no credential", func(t *testing.T) {
		w := doAPI(t.Context(), s.handler(), "GET", "/api/proxies", "")
		var got api.ProxyList
		decodeBody(t, w, http.StatusOK, &got)
		if g, want := jsonOf(t, got), jsonOf(t, api.ProxyList{Proxies: wantProxies(t, s.store)}); g != want {
			t.Errorf("list =\n%s\nwant\n%s", g, want)
		}
		assertNoSecret(t, w.Body.String())
	})
}

func TestImportProxiesAPI(t *testing.T) {
	var log bytes.Buffer
	s := newServeAPI(&log)
	s.store = newTestStore(t)
	steps := []struct {
		name, body string
		want       api.ProxyImport
	}{
		{"a new list", "# list\na.example:1:u:p\nsocks5h://b.example:2\n", api.ProxyImport{Added: 2}},
		// The last line of a.example:1 wins: it updates the credentials, and the earlier line counts as repeated.
		{"an update and a repeat", "a.example:1:x:y\na.example:1:u2:p2\nsocks5://b.example:2\nc.example:3\n", api.ProxyImport{Added: 1, Updated: 1, Repeated: 2}},
		{"an empty body", "", api.ProxyImport{}},
	}
	for _, step := range steps {
		log.Reset()
		var got api.ProxyImport
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/proxies/import", step.body), http.StatusOK, &got)
		if got != step.want {
			t.Errorf("%s: import = %+v, want %+v", step.name, got, step.want)
		}
		want := `level=INFO msg="proxies imported" added=` + strconv.Itoa(step.want.Added) + " updated=" + strconv.Itoa(step.want.Updated) +
			" repeated=" + strconv.Itoa(step.want.Repeated) + "\n"
		if log.String() != want {
			t.Errorf("%s: log = %q, want %q", step.name, log.String(), want)
		}
	}
	list := listProxies(t, s.handler())
	if len(list.Proxies) != 3 || !list.Proxies[0].Auth || list.Proxies[1].Proxy != "socks5://b.example:2" || list.Proxies[2].Auth {
		t.Errorf("list = %+v, want a.example:1 with auth, socks5://b.example:2 and c.example:3", list)
	}

	t.Run("an update keeps a disabled proxy disabled", func(t *testing.T) {
		if w := doAPI(t.Context(), s.handler(), "POST", "/api/proxies/3/disable", ""); w.Code != http.StatusOK {
			t.Fatalf("disable = %d %s", w.Code, w.Body.String())
		}
		var got api.ProxyImport
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/proxies/import", "c.example:3:u3:p3\n"), http.StatusOK, &got)
		if p := listProxies(t, s.handler()).Proxies[2]; got != (api.ProxyImport{Updated: 1}) || p.Enabled || !p.Auth {
			t.Errorf("import = %+v, proxy 3 = %+v; want one update of a disabled proxy with auth", got, p)
		}
	})
	t.Run("a bad line names its number only and changes nothing", func(t *testing.T) {
		before := listProxies(t, s.handler())
		log.Reset()
		w := doAPI(t.Context(), s.handler(), "POST", "/api/proxies/import", "d.example:4\na.example:1:secret9\n")
		assertAPIError(t, w, http.StatusBadRequest, "line 2: expected host:port or host:port:user:pass, got 3 parts")
		if strings.Contains(w.Body.String(), "secret9") || log.Len() != 0 {
			t.Errorf("answer %s, log %q; want no line text and no log", w.Body.String(), log.String())
		}
		if after := listProxies(t, s.handler()); jsonOf(t, after) != jsonOf(t, before) {
			t.Errorf("list after a bad import =\n%+v\nwant\n%+v", after, before)
		}
	})
	t.Run("a body over the bound", func(t *testing.T) {
		assertAPIError(t, doAPI(t.Context(), s.handler(), "POST", "/api/proxies/import", strings.Repeat("#", maxBodyBytes+1)),
			http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than %d bytes", maxBodyBytes))
	})
}

func TestExportProxies(t *testing.T) {
	s := newHistoryAPI(t)
	t.Run("no proxy", func(t *testing.T) {
		if w := doAPI(t.Context(), s.handler(), "GET", "/api/proxies/export", ""); w.Code != http.StatusOK || w.Body.Len() != 0 {
			t.Errorf("answer = %d %q, want 200 with an empty file", w.Code, w.Body.String())
		}
	})
	seedProxies(t, s.store)
	w := doAPI(t.Context(), s.handler(), "GET", "/api/proxies/export", "")
	const want = "a.example:1:user1:secret1\nsocks5://b.example:2\n"
	if w.Code != http.StatusOK || w.Body.String() != want {
		t.Fatalf("export = %d %q, want 200 %q", w.Code, w.Body.String(), want)
	}
	h := w.Header()
	if got := [2]string{h.Get("Content-Type"), h.Get("Content-Disposition")}; got != [2]string{"text/plain", `attachment; filename="proxies.txt"`} {
		t.Errorf("Content-Type, Content-Disposition = %q", got)
	}

	t.Run("an import of the file into the same store changes nothing", func(t *testing.T) {
		var got api.ProxyImport
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/proxies/import", want), http.StatusOK, &got)
		if got != (api.ProxyImport{Repeated: 2}) {
			t.Errorf("import = %+v, want two repeated", got)
		}
	})
	t.Run("an import of the file into a new store gives the same file", func(t *testing.T) {
		other := newHistoryAPI(t)
		var got api.ProxyImport
		decodeBody(t, doAPI(t.Context(), other.handler(), "POST", "/api/proxies/import", want), http.StatusOK, &got)
		again := doAPI(t.Context(), other.handler(), "GET", "/api/proxies/export", "")
		if got != (api.ProxyImport{Added: 2}) || again.Body.String() != want {
			t.Errorf("import = %+v, export %q; want two added and %q", got, again.Body.String(), want)
		}
	})
}

func TestSetProxyEnabledAPI(t *testing.T) {
	var log bytes.Buffer
	s := newServeAPI(&log)
	s.store = newTestStore(t)
	seedProxies(t, s.store)
	items := wantProxies(t, s.store)
	for _, step := range []struct {
		action  string
		enabled bool
	}{{"disable", false}, {"disable", false}, {"enable", true}, {"enable", true}} {
		log.Reset()
		w := doAPI(t.Context(), s.handler(), "POST", "/api/proxies/1/"+step.action, "")
		var got api.Proxy
		decodeBody(t, w, http.StatusOK, &got)
		want := items[0]
		want.Enabled = step.enabled
		if g, wj := jsonOf(t, got), jsonOf(t, want); g != wj {
			t.Errorf("%s answer =\n%s\nwant\n%s", step.action, g, wj)
		}
		assertNoSecret(t, w.Body.String())
		if wantLog := `level=INFO msg="proxy ` + step.action + `d" id=1` + "\n"; log.String() != wantLog {
			t.Errorf("%s log = %q, want %q", step.action, log.String(), wantLog)
		}
	}
	for _, action := range []string{"enable", "disable"} {
		for _, tt := range []struct {
			name, id string
			status   int
			want     string
		}{
			{"a proxy that does not exist", "99", http.StatusNotFound, "setting enabled of proxy 99: not found"},
			{"id 0", "0", http.StatusNotFound, "setting enabled of proxy 0: not found"},
			{"an id that is not an integer", "x", http.StatusBadRequest, "Invalid format for parameter id: "},
		} {
			t.Run(action+" "+tt.name, func(t *testing.T) {
				assertAPIError(t, doAPI(t.Context(), s.handler(), "POST", "/api/proxies/"+tt.id+"/"+action, ""), tt.status, tt.want)
			})
		}
	}
}

func TestDeleteProxyAPI(t *testing.T) {
	var log bytes.Buffer
	s := newServeAPI(&log)
	s.store = newTestStore(t)
	seedProxies(t, s.store)
	items := wantProxies(t, s.store)

	w := doAPI(t.Context(), s.handler(), "DELETE", "/api/proxies/1", "")
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("delete = %d %q, want 204 with no body", w.Code, w.Body.String())
	}
	if want := `level=INFO msg="proxy deleted" id=1` + "\n"; log.String() != want {
		t.Errorf("log = %q, want %q", log.String(), want)
	}
	if g, want := jsonOf(t, listProxies(t, s.handler())), jsonOf(t, api.ProxyList{Proxies: items[1:]}); g != want {
		t.Errorf("list after the delete =\n%s\nwant\n%s", g, want)
	}

	// The attempt rows stay, so an import of the same proxy shows its health again.
	if w := doAPI(t.Context(), s.handler(), "POST", "/api/proxies/import", "a.example:1\n"); w.Code != http.StatusOK {
		t.Fatalf("import = %d %s", w.Code, w.Body.String())
	}
	if got := listProxies(t, s.handler()).Proxies[1]; got.Id != 3 || got.Attempts != 2 || got.Successes != 1 {
		t.Errorf("proxy after a new import = %+v, want id 3 with its two attempts", got)
	}

	for _, tt := range []struct {
		name, id string
		status   int
		want     string
	}{
		{"a proxy that does not exist", "1", http.StatusNotFound, "deleting proxy 1: not found"},
		{"id 0", "0", http.StatusNotFound, "deleting proxy 0: not found"},
		{"an id that is not an integer", "x", http.StatusBadRequest, "Invalid format for parameter id: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertAPIError(t, doAPI(t.Context(), s.handler(), "DELETE", "/api/proxies/"+tt.id, ""), tt.status, tt.want)
		})
	}
}

// closedPort returns the address of a local port that refuses connections.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestTestProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := newHistoryAPI(t)
	text := ln.Addr().String() + ":user1:secret1\nsocks5://" + ln.Addr().String() + "\n" + closedPort(t) + ":user1:secret1\n"
	if _, err := s.store.ImportProxies(t.Context(), text); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, id  string
		ok        bool
		errPrefix string
	}{
		{"an HTTP upstream that listens", "1", true, ""},
		{"a SOCKS5 upstream that listens", "2", true, ""},
		{"an upstream that refuses", "3", false, "dialing upstream proxy: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := doAPI(t.Context(), s.handler(), "POST", "/api/proxies/"+tt.id+"/test", "")
			var got api.ProxyTest
			decodeBody(t, w, http.StatusOK, &got)
			gotErr := ""
			if got.Error != nil {
				gotErr = *got.Error
			}
			if got.Ok != tt.ok || got.DurationMs < 0 || (tt.ok && got.Error != nil) || !strings.HasPrefix(gotErr, tt.errPrefix) {
				t.Errorf("test = %s, want ok %v and the error prefix %q", w.Body.String(), tt.ok, tt.errPrefix)
			}
			assertNoSecret(t, w.Body.String())
		})
	}
	for _, tt := range []struct {
		name, id string
		status   int
		want     string
	}{
		{"a proxy that does not exist", "99", http.StatusNotFound, "reading proxy 99: not found"},
		{"id 0", "0", http.StatusNotFound, "reading proxy 0: not found"},
		{"an id that is not an integer", "x", http.StatusBadRequest, "Invalid format for parameter id: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertAPIError(t, doAPI(t.Context(), s.handler(), "POST", "/api/proxies/"+tt.id+"/test", ""), tt.status, tt.want)
		})
	}
}

// A store error answers 500 with the JSON error and logs one ERROR line.
func TestProxiesStoreErrors(t *testing.T) {
	const closed = "sql: database is closed"
	for _, tt := range []struct{ name, method, target, body, want string }{
		{"the list", "GET", "/api/proxies", "", "reading proxies: " + closed},
		{"the import", "POST", "/api/proxies/import", "a.example:1\n", "starting the proxy import transaction: " + closed},
		{"the export", "GET", "/api/proxies/export", "", "reading proxies: " + closed},
		{"the enable", "POST", "/api/proxies/1/enable", "", "setting enabled of proxy 1: " + closed},
		{"the disable", "POST", "/api/proxies/1/disable", "", "setting enabled of proxy 1: " + closed},
		{"the delete", "DELETE", "/api/proxies/1", "", "deleting proxy 1: " + closed},
		{"the test", "POST", "/api/proxies/1/test", "", "reading proxy: " + closed},
	} {
		t.Run(tt.name+" of a closed store", func(t *testing.T) {
			var log bytes.Buffer
			s := newServeAPI(&log)
			s.store = newTestStore(t)
			_ = s.store.Close()

			assertAPIError(t, doAPI(t.Context(), s.handler(), tt.method, tt.target, tt.body), http.StatusInternalServerError, tt.want)

			if want := `level=ERROR msg="answering the API request" path=` + tt.target + ` error=` + strconv.Quote(tt.want) + "\n"; log.String() != want {
				t.Errorf("log = %q, want %q", log.String(), want)
			}
		})
	}
}
