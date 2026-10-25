package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/store"
)

// historyT0 returns the start of the first seeded run.
func historyT0() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

// newHistoryAPI returns an apiServer over a new store with no run.
func newHistoryAPI(t *testing.T) *apiServer {
	t.Helper()
	s := newServeAPI(io.Discard)
	s.store = newTestStore(t)
	return s
}

// mustWriteRun saves a run of source that started at start with config and
// ends it at end with runErr. A zero end leaves the run in progress.
func mustWriteRun(t *testing.T, s *store.Store, source string, start, end time.Time, runErr string, config json.RawMessage) {
	t.Helper()
	id, err := s.WriteRun(t.Context(), source, start, config)
	if err == nil && !end.IsZero() {
		err = s.EndRun(t.Context(), id, end, runErr)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// seedHistory saves three runs with the config of cfg: run 1 of the CLI ended
// with an error and has two attempts, one with an account, run 2 of serve ended
// with no error and has no attempt, and run 3 of serve is in progress.
func seedHistory(t *testing.T, s *store.Store, cfg json.RawMessage) {
	t.Helper()
	mustWriteRun(t, s, "cli", historyT0(), historyT0().Add(time.Minute), "run failed", cfg)
	mustWriteRun(t, s, "serve", historyT0().Add(time.Hour), historyT0().Add(2*time.Hour), "", cfg)
	mustWriteRun(t, s, "serve", historyT0().Add(3*time.Hour), time.Time{}, "", cfg)
	ctx := t.Context()
	if err := s.WriteAccount(ctx, &account.Account{Email: "user@example.com", Username: "user", Password: "pw", Unconfirmed: true}); err != nil {
		t.Fatal(err)
	}
	saved := &store.Attempt{
		RunID: 1, Worker: 0, StartedAt: historyT0(), Duration: 3 * time.Second, Proxy: testProxyAddr,
		Steps:            []store.Step{{Name: "proxy_check", Duration: time.Second}, {Name: "submit_password", Duration: 2 * time.Second}},
		BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4,
	}
	failed := &store.Attempt{
		RunID: 1, Worker: 1, StartedAt: historyT0().Add(time.Second), Duration: 30 * time.Second,
		Category: "challenge_timeout", Error: "waiting for clearance: cloudflare challenge timed out", FailedStep: "challenge", FailedPhase: "browser",
		Steps: []store.Step{{Name: "challenge", Duration: 30 * time.Second}},
	}
	for _, w := range []struct {
		a     *store.Attempt
		email string
	}{{saved, "user@example.com"}, {failed, ""}} {
		if err := s.WriteAttempt(ctx, w.a, w.email); err != nil {
			t.Fatal(err)
		}
	}
}

// wantRuns returns the API form of the seeded runs, newest first.
func wantRuns(cfg api.Config) []api.Run {
	noCounts := api.RunCounts{Failures: map[string]int{}}
	return []api.Run{
		{Id: 3, Source: api.RunSourceServe, StartedAt: historyT0().Add(3 * time.Hour), Config: cfg, Counts: noCounts},
		{Id: 2, Source: api.RunSourceServe, StartedAt: historyT0().Add(time.Hour), EndedAt: new(historyT0().Add(2 * time.Hour)), Config: cfg, Counts: noCounts},
		{
			Id: 1, Source: api.RunSourceCLI, StartedAt: historyT0(), EndedAt: new(historyT0().Add(time.Minute)), Error: new("run failed"), Config: cfg,
			Counts: api.RunCounts{
				Attempts: 2, Accounts: 1, Unconfirmed: 1, Failures: map[string]int{"challenge_timeout": 1},
				BrowserBytesSent: 1, BrowserBytesReceived: 2, HttpBytesSent: 3, HttpBytesReceived: 4,
			},
		},
	}
}

// wantAttempts returns the API form of the attempts of seeded run 1, newest first.
func wantAttempts() []api.Attempt {
	return []api.Attempt{
		{
			Id: 2, RunId: 1, Worker: 1, StartedAt: historyT0().Add(time.Second), DurationMs: 30000,
			Category: new("challenge_timeout"), Error: new("waiting for clearance: cloudflare challenge timed out"),
			FailedStep: new("challenge"), FailedPhase: new("browser"), Steps: []api.AttemptStep{{Name: "challenge", DurationMs: 30000}},
		},
		{
			Id: 1, RunId: 1, Worker: 0, StartedAt: historyT0(), DurationMs: 3000, Proxy: new(testProxyAddr),
			Steps:     []api.AttemptStep{{Name: "proxy_check", DurationMs: 1000}, {Name: "submit_password", DurationMs: 2000}},
			AccountId: new(int64(1)), Email: new("user@example.com"), Username: new("user"),
			BrowserBytesSent: 1, BrowserBytesReceived: 2, HttpBytesSent: 3, HttpBytesReceived: 4,
		},
	}
}

// pageCase is one page request of a list endpoint.
type pageCase struct {
	name, query string
	wantIDs     []int64 // the ids of the page, on a 200
	wantNext    int64   // the next_before of a 200 page, 0 for an absent one
	wantStatus  int
	wantPrefix  string // the start of the message of a 400
}

// pageCases returns the paging and bound rows of a list with the three ids 3, 2 and 1.
func pageCases() []pageCase {
	return []pageCase{
		{name: "the first page with the default limit", wantIDs: []int64{3, 2, 1}},
		{name: "a page with more rows after it", query: "?limit=2", wantIDs: []int64{3, 2}, wantNext: 2},
		{name: "the last page from next_before", query: "?before=2&limit=2", wantIDs: []int64{1}},
		{name: "limit 1", query: "?limit=1", wantIDs: []int64{3}, wantNext: 3},
		{name: "limit 500", query: "?limit=500", wantIDs: []int64{3, 2, 1}},
		{name: "before 1 is an empty page", query: "?before=1", wantIDs: []int64{}},
		{name: "limit 0", query: "?limit=0", wantStatus: 400, wantPrefix: "limit must be from 1 to 500, got 0"},
		{name: "limit 501", query: "?limit=501", wantStatus: 400, wantPrefix: "limit must be from 1 to 500, got 501"},
		{name: "before 0", query: "?before=0", wantStatus: 400, wantPrefix: "before must be at least 1, got 0"},
		{name: "a negative before", query: "?before=-5", wantStatus: 400, wantPrefix: "before must be at least 1, got -5"},
		{name: "a limit that is not an integer", query: "?limit=ten", wantStatus: 400, wantPrefix: "Invalid format for parameter limit: "},
		{name: "a before that is not an integer", query: "?before=x", wantStatus: 400, wantPrefix: "Invalid format for parameter before: "},
	}
}

// runPageCases sends each of cases to path and reads the ids and the next_before of a 200 with ids.
func runPageCases(t *testing.T, s *apiServer, path string, cases []pageCase, ids func(t *testing.T, body []byte) ([]int64, *int64)) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			w := doAPI(t.Context(), s.handler(), "GET", path+tt.query, "")
			if tt.wantStatus == http.StatusBadRequest {
				var got api.Error
				decodeBody(t, w, http.StatusBadRequest, &got)
				if !strings.HasPrefix(got.Message, tt.wantPrefix) {
					t.Errorf("message = %q, want the prefix %q", got.Message, tt.wantPrefix)
				}
				return
			}
			var raw json.RawMessage
			decodeBody(t, w, http.StatusOK, &raw)
			gotIDs, next := ids(t, raw)
			var wantNext *int64
			if tt.wantNext != 0 {
				wantNext = new(tt.wantNext)
			}
			if g, w := jsonOf(t, gotIDs), jsonOf(t, tt.wantIDs); g != w || jsonOf(t, next) != jsonOf(t, wantNext) {
				t.Errorf("ids %s, next_before %s, want %s and %s", g, jsonOf(t, next), w, jsonOf(t, wantNext))
			}
		})
	}
}

// decodeJSON decodes body into v.
func decodeJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
}

func TestListRuns(t *testing.T) {
	s := newHistoryAPI(t)
	cfg := apiConfig(storedConfig(t, "stored"))
	seedHistory(t, s.store, json.RawMessage(jsonOf(t, cfg)))

	t.Run("each run with its config and its counts", func(t *testing.T) {
		var got api.RunPage
		decodeBody(t, doAPI(t.Context(), s.handler(), "GET", "/api/runs", ""), http.StatusOK, &got)
		if g, w := jsonOf(t, got), jsonOf(t, api.RunPage{Runs: wantRuns(cfg)}); g != w {
			t.Errorf("page =\n%s\nwant\n%s", g, w)
		}
	})
	runPageCases(t, s, "/api/runs", pageCases(), func(t *testing.T, body []byte) ([]int64, *int64) {
		t.Helper()
		var page api.RunPage
		decodeJSON(t, body, &page)
		ids := make([]int64, 0, len(page.Runs))
		for _, r := range page.Runs {
			ids = append(ids, r.Id)
		}
		return ids, page.NextBefore
	})
}

func TestListRunsDefaultLimit(t *testing.T) {
	s := newHistoryAPI(t)
	for range defaultPageLimit + 1 {
		mustWriteRun(t, s.store, "cli", historyT0(), time.Time{}, "", json.RawMessage("{}"))
	}

	var got api.RunPage
	decodeBody(t, doAPI(t.Context(), s.handler(), "GET", "/api/runs", ""), http.StatusOK, &got)

	if len(got.Runs) != 100 || got.Runs[0].Id != 101 || got.NextBefore == nil || *got.NextBefore != 2 {
		t.Errorf("%d runs from %d, next_before %v, want 100 runs from 101 and next_before 2", len(got.Runs), got.Runs[0].Id, got.NextBefore)
	}
}

func TestGetRunByID(t *testing.T) {
	s := newHistoryAPI(t)
	cfg := apiConfig(storedConfig(t, "stored"))
	seedHistory(t, s.store, json.RawMessage(jsonOf(t, cfg)))
	runs := wantRuns(cfg)

	for _, want := range runs {
		t.Run("run "+strconv.FormatInt(want.Id, 10), func(t *testing.T) {
			var got api.Run
			decodeBody(t, doAPI(t.Context(), s.handler(), "GET", "/api/runs/"+strconv.FormatInt(want.Id, 10), ""), http.StatusOK, &got)
			if g, w := jsonOf(t, got), jsonOf(t, want); g != w {
				t.Errorf("run =\n%s\nwant\n%s", g, w)
			}
		})
	}
	for _, tt := range []struct {
		name, path, want string
		status           int
	}{
		{"a run that does not exist", "/api/runs/99", "reading run 99: not found", http.StatusNotFound},
		{"an id that is not an integer", "/api/runs/x", "Invalid format for parameter id: ", http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got api.Error
			decodeBody(t, doAPI(t.Context(), s.handler(), "GET", tt.path, ""), tt.status, &got)
			if !strings.HasPrefix(got.Message, tt.want) {
				t.Errorf("message = %q, want the prefix %q", got.Message, tt.want)
			}
		})
	}
}

func TestListRunAttempts(t *testing.T) {
	s := newHistoryAPI(t)
	seedHistory(t, s.store, json.RawMessage("{}"))
	// A third attempt gives run 1 the three ids that the page cases page through.
	if err := s.store.WriteAttempt(t.Context(), &store.Attempt{RunID: 1, StartedAt: historyT0()}, ""); err != nil {
		t.Fatal(err)
	}

	t.Run("each attempt with its steps and its account", func(t *testing.T) {
		var got api.AttemptPage
		decodeBody(t, doAPI(t.Context(), s.handler(), "GET", "/api/runs/1/attempts?before=3", ""), http.StatusOK, &got)
		if g, w := jsonOf(t, got), jsonOf(t, api.AttemptPage{Attempts: wantAttempts()}); g != w {
			t.Errorf("page =\n%s\nwant\n%s", g, w)
		}
	})
	runPageCases(t, s, "/api/runs/1/attempts", pageCases(), func(t *testing.T, body []byte) ([]int64, *int64) {
		t.Helper()
		var page api.AttemptPage
		decodeJSON(t, body, &page)
		ids := make([]int64, 0, len(page.Attempts))
		for _, a := range page.Attempts {
			ids = append(ids, a.Id)
		}
		return ids, page.NextBefore
	})
	t.Run("a run with no attempt has an empty page", func(t *testing.T) {
		w := doAPI(t.Context(), s.handler(), "GET", "/api/runs/2/attempts", "")
		if want := `{"attempts":[]}` + "\n"; w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("answer = %d %s, want 200 %s", w.Code, w.Body.String(), want)
		}
	})
	for _, tt := range []struct {
		name, path, want string
		status           int
	}{
		{"a run that does not exist", "/api/runs/99/attempts", "reading run 99: not found", http.StatusNotFound},
		{"a page of a run that does not exist", "/api/runs/99/attempts?before=2&limit=1", "reading run 99: not found", http.StatusNotFound},
		{"an id that is not an integer", "/api/runs/x/attempts", "Invalid format for parameter id: ", http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got api.Error
			decodeBody(t, doAPI(t.Context(), s.handler(), "GET", tt.path, ""), tt.status, &got)
			if !strings.HasPrefix(got.Message, tt.want) {
				t.Errorf("message = %q, want the prefix %q", got.Message, tt.want)
			}
		})
	}
}

// A store error answers 500 with the JSON error and logs one ERROR line, and the run handlers send no partial page.
func TestHistoryStoreErrors(t *testing.T) {
	const configErr = `reading the config of run 1: json: cannot unmarshal string into Go struct field Config.debug of type bool`
	tests := []struct {
		name, path, want string
		badConfig        bool // the store holds run 1 with a config that does not decode. Else the store is closed
	}{
		{name: "the runs of a closed store", path: "/api/runs", want: "reading runs: sql: database is closed"},
		{name: "a run of a closed store", path: "/api/runs/1", want: "reading run: sql: database is closed"},
		{name: "the attempts of a closed store", path: "/api/runs/1/attempts", want: "reading attempts: sql: database is closed"},
		{name: "the runs with a saved config that does not decode", path: "/api/runs", want: configErr, badConfig: true},
		{name: "a run with a saved config that does not decode", path: "/api/runs/1", want: configErr, badConfig: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := newServeAPI(&buf)
			s.store = newTestStore(t)
			if tt.badConfig {
				mustWriteRun(t, s.store, "cli", historyT0(), time.Time{}, "", json.RawMessage(`{"debug":"yes"}`))
			} else {
				_ = s.store.Close()
			}

			var got api.Error
			decodeBody(t, doAPI(t.Context(), s.handler(), "GET", tt.path, ""), http.StatusInternalServerError, &got)

			if got.Message != tt.want {
				t.Errorf("message = %q, want %q", got.Message, tt.want)
			}
			if want := `level=ERROR msg="answering the API request" path=` + tt.path + ` error=` + strconv.Quote(tt.want) + "\n"; buf.String() != want {
				t.Errorf("log = %q, want %q", buf.String(), want)
			}
		})
	}
	t.Run("a counts read on a closed store", func(t *testing.T) {
		s := newHistoryAPI(t)
		_ = s.store.Close()
		if _, err := s.apiRuns(t.Context(), []store.Run{{ID: 1}}); err == nil || !strings.HasSuffix(err.Error(), "sql: database is closed") {
			t.Errorf("apiRuns() = %v, want a closed database", err)
		}
	})
}

// newRunLogAPI returns an apiServer that logs to buf, with a database in a temp
// dir that holds run 1, and the log file of run 1 with two lines.
func newRunLogAPI(t *testing.T, buf io.Writer) (*apiServer, *slog.Logger) {
	t.Helper()
	s := newServeAPI(buf)
	s.store = newTestStore(t)
	s.cfg = tempConfig(t, "")
	mustWriteRun(t, s.store, "serve", historyT0(), time.Time{}, "", json.RawMessage("{}"))
	_, logger := openServeRunLog(t, s, 1)
	logger.Info("first", "s", "a b")
	logger.Info("second")
	return s, logger
}

// getRunLog returns the answer of s to a download of the log of run 1.
func getRunLog(t *testing.T, s *apiServer) api.GetRunLogResponseObject {
	t.Helper()
	resp, err := s.GetRunLog(t.Context(), api.GetRunLogRequestObject{Id: 1})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestGetRunLog(t *testing.T) {
	t.Run("the file as an attachment, with its size, the deadline moved on the write", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, logger := newRunLogAPI(t, io.Discard)
			file := readRunLog(t, s.cfg, 1)
			resp := getRunLog(t, s)
			// A run in progress appends after the request. The answer stops at the size of the request.
			logger.Info("after the request")
			w := newStreamWriter()

			if err := resp.VisitGetRunLogResponse(w); err != nil {
				t.Fatalf("Visit() = %v, want nil", err)
			}

			got := w.read()
			if got.status != http.StatusOK || got.body != file {
				t.Errorf("status %d, body %q, want 200 and the file at the request\n%s", got.status, got.body, file)
			}
			for k, v := range map[string]string{
				"Content-Type":        "text/plain; charset=utf-8",
				"Content-Length":      strconv.Itoa(len(file)),
				"Content-Disposition": `attachment; filename="run-1.log"`,
			} {
				if h := got.header.Get(k); h != v {
					t.Errorf("%s = %q, want %q", k, h, v)
				}
			}
			if !got.deadline.Equal(time.Now().Add(apiWriteTimeout)) {
				t.Errorf("deadline = %v, want now plus %v", got.deadline, apiWriteTimeout)
			}
		})
	})

	t.Run("a write that fails returns its error", func(t *testing.T) {
		for _, fail := range []func(w *streamWriter){
			func(w *streamWriter) { w.failWrite = true },
			func(w *streamWriter) { w.failDeadline = true },
		} {
			s, _ := newRunLogAPI(t, io.Discard)
			w := newStreamWriter()
			fail(w)

			err := getRunLog(t, s).VisitGetRunLogResponse(w)

			if err == nil || !strings.HasPrefix(err.Error(), "sending the run log: ") {
				t.Errorf("Visit() = %v, want the send error", err)
			}
		}
	})

	t.Run("a run or a file that does not exist gets 404", func(t *testing.T) {
		s, _ := newRunLogAPI(t, io.Discard)
		mustWriteRun(t, s.store, "cli", historyT0(), time.Time{}, "", json.RawMessage("{}"))
		for path, want := range map[string]string{
			"/api/runs/9/log": "reading run 9: not found",
			"/api/runs/2/log": "run 2 has no log file",
		} {
			var got api.Error
			decodeBody(t, doAPI(t.Context(), s.handler(), "GET", path, ""), http.StatusNotFound, &got)
			if got.Message != want {
				t.Errorf("GET %s message = %q, want %q", path, got.Message, want)
			}
		}
	})

	t.Run("a failed read gets 500", func(t *testing.T) {
		tests := []struct {
			name, wantPrefix string
			breakIt          func(s *apiServer)
		}{
			{"a closed store", "reading run: sql: database is closed", func(s *apiServer) { _ = s.store.Close() }},
			// A NUL byte is a path that no OS opens.
			{"a path that does not open", "opening the run log: ", func(s *apiServer) { s.cfg.Database = "bad\x00/creator.db" }},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var buf bytes.Buffer
				s, _ := newRunLogAPI(t, &buf)
				tt.breakIt(s)

				var got api.Error
				decodeBody(t, doAPI(t.Context(), s.handler(), "GET", "/api/runs/1/log", ""), http.StatusInternalServerError, &got)

				if !strings.HasPrefix(got.Message, tt.wantPrefix) || !strings.Contains(buf.String(), `msg="answering the API request" path=/api/runs/1/log`) {
					t.Errorf("message = %q, log %q, want the prefix %q and the ERROR line", got.Message, buf.String(), tt.wantPrefix)
				}
			})
		}
	})
}
