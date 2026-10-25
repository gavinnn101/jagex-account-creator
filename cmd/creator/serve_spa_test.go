package main

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/logtest"
)

const (
	dashboardIndex = "<!doctype html><title>dashboard</title>"
	dashboardAsset = "console.log(1)"
)

// builtDashboard returns the files of a built dashboard, as web.SPA does.
func builtDashboard() (fs.FS, error) {
	return fstest.MapFS{
		"index.html":              {Data: []byte(dashboardIndex)},
		"_app/immutable/entry.js": {Data: []byte(dashboardAsset)},
	}, nil
}

// newSPAAPI returns the routes of an apiServer that serves builtDashboard.
func newSPAAPI(t *testing.T) http.Handler {
	t.Helper()
	spa, err := builtDashboard()
	if err != nil {
		t.Fatal(err)
	}
	return (&apiServer{cfg: storedConfig(t, ""), logger: logtest.New(io.Discard), spa: spa}).handler()
}

func TestSPAHandler(t *testing.T) {
	const html = "text/html; charset=utf-8"
	tests := []struct {
		name, method, path string
		want               int
		wantType, wantBody string
	}{
		{name: "the root", method: "GET", path: "/", want: 200, wantType: html, wantBody: dashboardIndex},
		{name: "a client route", method: "GET", path: "/runs/7", want: 200, wantType: html, wantBody: dashboardIndex},
		{name: "a client route by HEAD", method: "HEAD", path: "/runs/7", want: 200, wantType: html},
		{name: "a missing path under _app", method: "GET", path: "/_app/immutable/missing.js", want: 404, wantType: "text/plain; charset=utf-8", wantBody: "404 page not found\n"},
		{name: "a dir under _app gets no listing", method: "GET", path: "/_app/immutable", want: 404, wantType: "text/plain; charset=utf-8", wantBody: "404 page not found\n"},
		{name: "a dir path with a slash under _app", method: "GET", path: "/_app/immutable/", want: 404, wantType: "text/plain; charset=utf-8", wantBody: "404 page not found\n"},
		{name: "a POST of a client route", method: "POST", path: "/runs/7", want: 405, wantType: "text/plain; charset=utf-8", wantBody: "Method Not Allowed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doAPI(t.Context(), newSPAAPI(t), tt.method, tt.path, "")

			if w.Code != tt.want || w.Header().Get("Content-Type") != tt.wantType || w.Body.String() != tt.wantBody {
				t.Errorf("answer = %d %q %q, want %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String(), tt.want, tt.wantType, tt.wantBody)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want the nosniff of guardAPI", got)
			}
		})
	}
}

// The content type comes from the mime table of the OS, which on Windows reads the registry.
func TestSPAHandlerAsset(t *testing.T) {
	w := doAPI(t.Context(), newSPAAPI(t), "GET", "/_app/immutable/entry.js", "")

	ct := w.Header().Get("Content-Type")
	if w.Code != http.StatusOK || w.Body.String() != dashboardAsset {
		t.Errorf("answer = %d %q, want 200 %q", w.Code, w.Body.String(), dashboardAsset)
	}
	if !strings.HasPrefix(ct, "text/javascript") && !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript or application/javascript", ct)
	}
}

// The app shell has one URL: http.FileServer sends /index.html to ./, also for a client route.
func TestSPAHandlerIndexRedirect(t *testing.T) {
	for _, path := range []string{"/index.html", "/runs/index.html"} {
		t.Run(path, func(t *testing.T) {
			w := doAPI(t.Context(), newSPAAPI(t), "GET", path, "")

			if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "./" {
				t.Errorf("answer = %d Location %q, want 301 to ./", w.Code, w.Header().Get("Location"))
			}
		})
	}
}

func TestSPAHandlerAPIPaths(t *testing.T) {
	t.Run("an unknown GET gets the JSON 404", func(t *testing.T) {
		var got api.Error
		decodeBody(t, doAPI(t.Context(), newSPAAPI(t), "GET", "/api/x", ""), http.StatusNotFound, &got)

		if want := "no API endpoint at /api/x"; got.Message != want {
			t.Errorf("message = %q, want %q", got.Message, want)
		}
	})
	// The mux sends the root of the GET /api/ subtree to it, so /api never reaches the index.html of GET /.
	t.Run("a GET of /api with no slash goes to /api/", func(t *testing.T) {
		w := doAPI(t.Context(), newSPAAPI(t), "GET", "/api", "")

		if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/api/" {
			t.Errorf("answer = %d Location %q, want 307 to /api/", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("a known route keeps its handler", func(t *testing.T) {
		var got api.Config
		decodeBody(t, doAPI(t.Context(), newSPAAPI(t), "GET", "/api/config", ""), http.StatusOK, &got)
	})
	tests := []struct {
		name, method, path, wantAllow string
	}{
		{name: "a known path keeps its 405", method: "PUT", path: "/api/run", wantAllow: "GET, HEAD, POST"},
		{name: "an unknown POST gets the 405 of the mux", method: "POST", path: "/api/x", wantAllow: "GET, HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doAPI(t.Context(), newSPAAPI(t), tt.method, tt.path, "")

			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != tt.wantAllow {
				t.Errorf("answer = %d Allow %q, want 405 Allow %q", w.Code, w.Header().Get("Allow"), tt.wantAllow)
			}
		})
	}
}
