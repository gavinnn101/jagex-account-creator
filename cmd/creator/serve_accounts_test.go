package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/store"
)

// secondProxyAddr is the proxy of seeded account 3.
const secondProxyAddr = "198.51.100.2:3128"

// accountsT0 returns the created_at of the first seeded account.
func accountsT0() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

// seedAccounts saves three accounts, one hour apart. Run 1 saved account 1,
// which has its 2FA, a proxy and byte counts. Run 2 saved account 2, which is
// unconfirmed and has a TOTP secret with no backup codes. No attempt links
// account 3, which has no 2FA.
func seedAccounts(t *testing.T, s *store.Store) {
	t.Helper()
	accounts := []account.Account{
		{
			Email: "a@example.com", Username: "alpha", Password: "pa", TOTPSecret: "SECRETA", BackupCodes: []string{"c1", "c2"},
			CreatedAt: accountsT0(), Proxy: testProxyAddr, BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4,
		},
		{Email: "b@example.com", Username: "Bravo", Password: "pb", TOTPSecret: "SECRETB", CreatedAt: accountsT0().Add(time.Hour), Unconfirmed: true},
		{Email: "c@example.com", Username: "charlie", Password: "pc", CreatedAt: accountsT0().Add(2 * time.Hour), Proxy: secondProxyAddr},
	}
	for i := range accounts {
		if err := s.WriteAccount(t.Context(), &accounts[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i, email := range []string{"a@example.com", "b@example.com"} {
		mustWriteRun(t, s, "serve", accountsT0(), time.Time{}, "", json.RawMessage("{}"))
		if err := s.WriteAttempt(t.Context(), &store.Attempt{RunID: int64(i + 1), StartedAt: accountsT0()}, email); err != nil {
			t.Fatal(err)
		}
	}
}

// seedBadSecret saves account 4, whose TOTP secret does not decode. No attempt links it.
func seedBadSecret(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.WriteAccount(t.Context(), &account.Account{Email: "d@example.com", Username: "delta", Password: "pd", TOTPSecret: "!!!", CreatedAt: accountsT0()}); err != nil {
		t.Fatal(err)
	}
}

// wantSummaries returns the list form of the seeded accounts, newest first.
func wantSummaries() []api.AccountSummary {
	return []api.AccountSummary{
		{Id: 3, Email: "c@example.com", Username: "charlie", CreatedAt: accountsT0().Add(2 * time.Hour), Proxy: new(secondProxyAddr)},
		{Id: 2, RunId: new(int64(2)), Email: "b@example.com", Username: "Bravo", CreatedAt: accountsT0().Add(time.Hour), Totp: true, Unconfirmed: new(true)},
		{
			Id: 1, RunId: new(int64(1)), Email: "a@example.com", Username: "alpha", CreatedAt: accountsT0(), Totp: true, Proxy: new(testProxyAddr),
			BrowserBytesSent: new(int64(1)), BrowserBytesReceived: new(int64(2)), HttpBytesSent: new(int64(3)), HttpBytesReceived: new(int64(4)),
		},
	}
}

// accountIDs reads the ids and the next_before of an account page.
func accountIDs(t *testing.T, body []byte) ([]int64, *int64) {
	t.Helper()
	var page api.AccountPage
	decodeJSON(t, body, &page)
	ids := make([]int64, 0, len(page.Accounts))
	for _, a := range page.Accounts {
		ids = append(ids, a.Id)
	}
	return ids, page.NextBefore
}

// accountFilterCases returns the filter rows of the list over the seeded accounts.
func accountFilterCases() []pageCase {
	const bad = http.StatusBadRequest
	return []pageCase{
		{name: "run 1", query: "?run=1", wantIDs: []int64{1}},
		{name: "run 2", query: "?run=2", wantIDs: []int64{2}},
		{name: "a run with no account", query: "?run=9", wantIDs: []int64{}},
		{name: "unconfirmed", query: "?unconfirmed=true", wantIDs: []int64{2}},
		{name: "confirmed", query: "?unconfirmed=false", wantIDs: []int64{3, 1}},
		{name: "with a TOTP secret", query: "?totp=true", wantIDs: []int64{2, 1}},
		{name: "without a TOTP secret", query: "?totp=false", wantIDs: []int64{3}},
		{name: "a proxy", query: "?proxy=" + url.QueryEscape(testProxyAddr), wantIDs: []int64{1}},
		// The stored text has nine fraction digits, so a bound with none tests the compare in the stored form.
		{name: "created_from is inclusive", query: "?created_from=2026-01-02T04:04:05Z", wantIDs: []int64{3, 2}},
		{name: "created_to is exclusive", query: "?created_to=2026-01-02T04:04:05Z", wantIDs: []int64{1}},
		{name: "created_from with an offset", query: "?created_from=" + url.QueryEscape("2026-01-02T05:04:05+01:00"), wantIDs: []int64{3, 2}},
		{name: "created_to with fraction digits", query: "?created_to=2026-01-02T04:04:05.000000001Z", wantIDs: []int64{2, 1}},
		{name: "a search in another case", query: "?q=BRAVO", wantIDs: []int64{2}},
		{name: "a search of the email", query: "?q=" + url.QueryEscape("c@"), wantIDs: []int64{3}},
		{name: "a search that every account matches", query: "?q=example.com", wantIDs: []int64{3, 2, 1}},
		{name: "a search with a LIKE wildcard", query: "?q=" + url.QueryEscape("%"), wantIDs: []int64{}},
		{name: "a combined filter", query: "?totp=true&unconfirmed=false", wantIDs: []int64{1}},
		{name: "a filtered page with more rows after it", query: "?totp=true&limit=1", wantIDs: []int64{2}, wantNext: 2},
		{name: "run 0", query: "?run=0", wantStatus: bad, wantPrefix: "run must be at least 1, got 0"},
		{name: "a negative run", query: "?limit=2&run=-1", wantStatus: bad, wantPrefix: "run must be at least 1, got -1"},
		{name: "a run that is not an integer", query: "?run=x", wantStatus: bad, wantPrefix: "Invalid format for parameter run: "},
		{name: "an unconfirmed that is not a bool", query: "?unconfirmed=maybe", wantStatus: bad, wantPrefix: "Invalid format for parameter unconfirmed: "},
		{name: "a totp that is not a bool", query: "?totp=2", wantStatus: bad, wantPrefix: "Invalid format for parameter totp: "},
		{name: "a created_from that is not a date-time", query: "?created_from=yesterday", wantStatus: bad, wantPrefix: "Invalid format for parameter created_from: "},
		{name: "a created_to that is not a date-time", query: "?created_to=noon", wantStatus: bad, wantPrefix: "Invalid format for parameter created_to: "},
	}
}

// assertAPIError checks that w is the JSON error with status, whose message starts with prefix.
func assertAPIError(t *testing.T, w *httptest.ResponseRecorder, status int, prefix string) {
	t.Helper()
	var got api.Error
	// The decode fails on any byte after the JSON error, so it also shows that no part of a file went out.
	decodeBody(t, w, status, &got)
	if !strings.HasPrefix(got.Message, prefix) {
		t.Errorf("message = %q, want the prefix %q", got.Message, prefix)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition = %q on an error, want none", cd)
	}
}

func TestListAccounts(t *testing.T) {
	s := newHistoryAPI(t)
	seedAccounts(t, s.store)

	t.Run("each account with no secret", func(t *testing.T) {
		w := doAPI(t.Context(), s.handler(), "GET", "/api/accounts", "")
		var got api.AccountPage
		decodeBody(t, w, http.StatusOK, &got)
		if g, want := jsonOf(t, got), jsonOf(t, api.AccountPage{Accounts: wantSummaries(), Total: 3}); g != want {
			t.Errorf("page =\n%s\nwant\n%s", g, want)
		}
		for _, key := range []string{`"password"`, `"totp_secret"`, `"backup_codes"`, "SECRETA"} {
			if strings.Contains(w.Body.String(), key) {
				t.Errorf("the list holds %s:\n%s", key, w.Body.String())
			}
		}
	})
	// Two accounts have a TOTP secret. Each page of one account holds the total of both.
	for _, query := range []string{"?totp=true&limit=1", "?totp=true&limit=1&before=2"} {
		t.Run("the total of a filter, "+query, func(t *testing.T) {
			var got api.AccountPage
			decodeBody(t, doAPI(t.Context(), s.handler(), "GET", "/api/accounts"+query, ""), http.StatusOK, &got)
			if len(got.Accounts) != 1 || got.Total != 2 {
				t.Errorf("%d accounts, total %d, want 1 account, total 2", len(got.Accounts), got.Total)
			}
		})
	}
	runPageCases(t, s, "/api/accounts", append(pageCases(), accountFilterCases()...), accountIDs)
}

// The bubble clock starts at 2000-01-01T00:00:00Z, a window start. accountsWait moves it 7 s into the window.
const accountsWait = 7 * time.Second

// accountsCode is the TOTP code of SECRETA and of SECRETB after accountsWait. Base32 drops the low bits of the
// last letter, so both decode to one key.
const accountsCode = "015777"

func TestGetAccountByID(t *testing.T) {
	sums := wantSummaries()
	full := api.Account{
		Id: 1, RunId: sums[2].RunId, Email: "a@example.com", Username: "alpha", Password: "pa", CreatedAt: accountsT0(),
		Totp: true, TotpSecret: new("SECRETA"), BackupCodes: new([]string{"c1", "c2"}), Proxy: new(testProxyAddr),
		BrowserBytesSent: sums[2].BrowserBytesSent, BrowserBytesReceived: sums[2].BrowserBytesReceived,
		HttpBytesSent: sums[2].HttpBytesSent, HttpBytesReceived: sums[2].HttpBytesReceived,
		TotpCode: new(accountsCode), TotpRemainingSeconds: new(23),
	}
	unconfirmed := api.Account{
		Id: 2, RunId: sums[1].RunId, Email: "b@example.com", Username: "Bravo", Password: "pb", CreatedAt: sums[1].CreatedAt,
		Totp: true, TotpSecret: new("SECRETB"), Unconfirmed: new(true), TotpCode: new(accountsCode), TotpRemainingSeconds: new(23),
	}
	no2FA := api.Account{Id: 3, Email: "c@example.com", Username: "charlie", Password: "pc", CreatedAt: sums[0].CreatedAt, Proxy: new(secondProxyAddr)}
	badSecret := api.Account{Id: 4, Email: "d@example.com", Username: "delta", Password: "pd", CreatedAt: accountsT0(), Totp: true, TotpSecret: new("!!!")}

	for _, want := range []api.Account{full, unconfirmed, no2FA, badSecret} {
		t.Run("account "+strconv.FormatInt(want.Id, 10), func(t *testing.T) {
			// The fake clock of the bubble fixes the TOTP code and its seconds left.
			synctest.Test(t, func(t *testing.T) {
				s := newHistoryAPI(t)
				seedAccounts(t, s.store)
				seedBadSecret(t, s.store)
				time.Sleep(accountsWait)
				var got api.Account
				decodeBody(t, doAPI(t.Context(), s.handler(), "GET", "/api/accounts/"+strconv.FormatInt(want.Id, 10), ""), http.StatusOK, &got)
				if g, w := jsonOf(t, got), jsonOf(t, want); g != w {
					t.Errorf("account =\n%s\nwant\n%s", g, w)
				}
			})
		})
	}
	s := newHistoryAPI(t)
	seedAccounts(t, s.store)
	t.Run("an account that does not exist", func(t *testing.T) {
		assertAPIError(t, doAPI(t.Context(), s.handler(), "GET", "/api/accounts/99", ""), http.StatusNotFound, "reading account 99: not found")
	})
	t.Run("an id that is not an integer", func(t *testing.T) {
		assertAPIError(t, doAPI(t.Context(), s.handler(), "GET", "/api/accounts/x", ""), http.StatusBadRequest, "Invalid format for parameter id: ")
	})
}

func TestConfirmAccount(t *testing.T) {
	t.Run("an unconfirmed account, two times", func(t *testing.T) {
		var log bytes.Buffer
		s := newServeAPI(&log)
		s.store = newTestStore(t)
		seedAccounts(t, s.store)
		want := wantSummaries()[1]
		want.Unconfirmed = nil
		for range 2 {
			var got api.AccountSummary
			decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/accounts/2/confirm", ""), http.StatusOK, &got)
			if g, w := jsonOf(t, got), jsonOf(t, want); g != w {
				t.Errorf("confirm answer =\n%s\nwant\n%s", g, w)
			}
		}
		if wantLog := strings.Repeat("level=INFO msg=\"account confirmed\" id=2\n", 2); log.String() != wantLog {
			t.Errorf("log = %q, want %q", log.String(), wantLog)
		}
		ids, _ := accountIDs(t, doAPI(t.Context(), s.handler(), "GET", "/api/accounts?unconfirmed=true", "").Body.Bytes())
		if len(ids) != 0 {
			t.Errorf("unconfirmed accounts after the confirm = %v, want none", ids)
		}
	})
	s := newHistoryAPI(t)
	seedAccounts(t, s.store)
	for _, tt := range []struct {
		name, id string
		status   int
		want     string
	}{
		{"an account that does not exist", "99", http.StatusNotFound, "confirming account 99: not found"},
		{"id 0", "0", http.StatusNotFound, "confirming account 0: not found"},
		{"a negative id", "-1", http.StatusNotFound, "confirming account -1: not found"},
		{"an id that is not an integer", "x", http.StatusBadRequest, "Invalid format for parameter id: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertAPIError(t, doAPI(t.Context(), s.handler(), "POST", "/api/accounts/"+tt.id+"/confirm", ""), tt.status, tt.want)
		})
	}
}

// storeExport returns what export writes for f.
func storeExport(t *testing.T, s *store.Store, export func(*store.Store, context.Context, io.Writer, store.AccountFilter) error, f store.AccountFilter) string {
	t.Helper()
	var buf bytes.Buffer
	if err := export(s, t.Context(), &buf, f); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestExportAccounts(t *testing.T) {
	s := newHistoryAPI(t)
	seedAccounts(t, s.store)
	const ndjson, csv, text = "application/x-ndjson", "text/csv", "text/plain"
	tests := []struct {
		name, query string
		export      func(*store.Store, context.Context, io.Writer, store.AccountFilter) error // nil for a fixed want
		filter      store.AccountFilter
		want        string // the body when export is nil
		wantType    string
		wantFile    string
		sized       bool // the media type sends a Content-Length
	}{
		{name: "jsonl", query: "?format=jsonl", export: (*store.Store).ExportJSONL, wantType: ndjson, wantFile: "accounts.jsonl", sized: true},
		{name: "csv", query: "?format=csv", export: (*store.Store).ExportCSV, wantType: csv, wantFile: "accounts.csv", sized: true},
		{name: "userpass", query: "?format=userpass", want: "a@example.com:pa\nb@example.com:pb\nc@example.com:pc\n", wantType: text, wantFile: "accounts.txt"},
		{
			name: "jsonl with a filter", query: "?format=jsonl&totp=true", export: (*store.Store).ExportJSONL,
			filter: store.AccountFilter{HasTOTP: new(true)}, wantType: ndjson, wantFile: "accounts.jsonl", sized: true,
		},
		{
			name: "csv with a filter", query: "?format=csv&run=1", export: (*store.Store).ExportCSV,
			filter: store.AccountFilter{RunID: 1}, wantType: csv, wantFile: "accounts.csv", sized: true,
		},
		{name: "userpass with a search", query: "?format=userpass&q=BRAVO", want: "b@example.com:pb\n", wantType: text, wantFile: "accounts.txt"},
		{
			name: "userpass with a time bound and a filter", query: "?format=userpass&created_from=2026-01-02T04:04:05Z&unconfirmed=false",
			want: "c@example.com:pc\n", wantType: text, wantFile: "accounts.txt",
		},
		{name: "jsonl with no match", query: "?format=jsonl&q=zzz", wantType: ndjson, wantFile: "accounts.jsonl", sized: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if tt.export != nil {
				want = storeExport(t, s.store, tt.export, tt.filter)
			}
			w := doAPI(t.Context(), s.handler(), "GET", "/api/accounts/export"+tt.query, "")

			if w.Code != http.StatusOK || w.Body.String() != want {
				t.Errorf("answer = %d\n%s\nwant 200\n%s", w.Code, w.Body.String(), want)
			}
			// An empty body sends no Content-Length here. net/http then sets 0.
			wantLength := ""
			if tt.sized && want != "" {
				wantLength = strconv.Itoa(len(want))
			}
			h := w.Header()
			if got := [3]string{h.Get("Content-Type"), h.Get("Content-Disposition"), h.Get("Content-Length")}; got != [3]string{tt.wantType, `attachment; filename="` + tt.wantFile + `"`, wantLength} {
				t.Errorf("Content-Type, Content-Disposition, Content-Length = %q, want %q, %q, %q", got, tt.wantType, tt.wantFile, wantLength)
			}
		})
	}
	for _, tt := range []struct{ name, query, want string }{
		{"a format that is not known", "?format=xml", `format must be jsonl, csv or userpass, got "xml"`},
		{"an empty format", "?format=", `format must be jsonl, csv or userpass, got ""`},
		{"no format", "", "Query argument format is required, but not found"},
		{"run 0", "?format=csv&run=0", "run must be at least 1, got 0"},
		{"a filter that does not parse", "?format=jsonl&totp=maybe", "Invalid format for parameter totp: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertAPIError(t, doAPI(t.Context(), s.handler(), "GET", "/api/accounts/export"+tt.query, ""), http.StatusBadRequest, tt.want)
		})
	}
}

// A store error answers 500 with the JSON error and logs one ERROR line. The export sends no part of a file.
func TestAccountsStoreErrors(t *testing.T) {
	const closed = "sql: database is closed"
	for _, tt := range []struct{ name, method, target, want string }{
		{"the list", "GET", "/api/accounts", "reading accounts: " + closed},
		{"one account", "GET", "/api/accounts/1", "reading account: " + closed},
		{"the confirm", "POST", "/api/accounts/1/confirm", "confirming account 1: " + closed},
		{"the jsonl export", "GET", "/api/accounts/export?format=jsonl", "reading accounts: " + closed},
		{"the csv export", "GET", "/api/accounts/export?format=csv", "reading accounts: " + closed},
		{"the userpass export", "GET", "/api/accounts/export?format=userpass", "reading accounts: " + closed},
	} {
		t.Run(tt.name+" of a closed store", func(t *testing.T) {
			var log bytes.Buffer
			s := newServeAPI(&log)
			s.store = newTestStore(t)
			_ = s.store.Close()

			assertAPIError(t, doAPI(t.Context(), s.handler(), tt.method, tt.target, ""), http.StatusInternalServerError, tt.want)

			path, _, _ := strings.Cut(tt.target, "?")
			if want := `level=ERROR msg="answering the API request" path=` + path + ` error=` + strconv.Quote(tt.want) + "\n"; log.String() != want {
				t.Errorf("log = %q, want %q", log.String(), want)
			}
		})
	}
	// The jsonl export writes each line as it converts it, so only the buffer keeps the first line from the client.
	t.Run("a jsonl export that fails after its first line", func(t *testing.T) {
		cfg, _ := exportPaths(t)
		st, err := openStore(t.Context(), logtest.New(io.Discard), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		seedAccounts(t, st)
		db, err := sql.Open("sqlite", cfg.Database)
		if err == nil {
			_, err = db.ExecContext(t.Context(), `INSERT INTO accounts (email, username, password, created_at) VALUES ('z@example.com', 'z', 'pz', 'yesterday')`)
			err = errors.Join(err, db.Close())
		}
		if err != nil {
			t.Fatal(err)
		}
		var log bytes.Buffer
		s := newServeAPI(&log)
		s.store = st

		w := doAPI(t.Context(), s.handler(), "GET", "/api/accounts/export?format=jsonl", "")

		assertAPIError(t, w, http.StatusInternalServerError, "reading created_at of z@example.com: ")
		if !strings.Contains(log.String(), `level=ERROR msg="answering the API request" path=/api/accounts/export`) {
			t.Errorf("log = %q, want the ERROR line", log.String())
		}
	})
}

// The API export with no filter writes the bytes of the CLI export.
func TestExportAccountsMatchesCLI(t *testing.T) {
	cfg, out := exportPaths(t)
	st, err := openStore(t.Context(), logtest.New(io.Discard), cfg)
	if err != nil {
		t.Fatal(err)
	}
	seedAccounts(t, st)
	s := newServeAPI(io.Discard)
	s.store = st
	w := doAPI(t.Context(), s.handler(), "GET", "/api/accounts/export?format=jsonl", "")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if err := runExport(t.Context(), logtest.New(io.Discard), cfg, out); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	if got := readTestFile(t, out); w.Code != http.StatusOK || got != w.Body.String() || strings.Count(got, "\n") != 3 {
		t.Errorf("CLI export =\n%s\nwant the three lines of the API export %d\n%s", got, w.Code, w.Body.String())
	}
}
