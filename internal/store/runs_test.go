package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/logtest"
)

// runConfigText is the config of each run that mustWriteRun writes.
const runConfigText = `{"debug":false}`

// plus2 is a zone east of UTC, so a test sees the conversion to UTC.
func plus2() *time.Location { return time.FixedZone("+02:00", 2*60*60) }

// runStart is the start of each run that mustWriteRun writes.
func runStart() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 6, plus2()) }

func mustWriteRun(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.WriteRun(t.Context(), "cli", runStart(), json.RawMessage(runConfigText))
	if err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	return id
}

func mustWriteAttempt(t *testing.T, s *Store, a *Attempt, email string) {
	t.Helper()
	if err := s.WriteAttempt(t.Context(), a, email); err != nil {
		t.Fatalf("WriteAttempt: %v", err)
	}
}

func mustWriteAccount(t *testing.T, s *Store, email, username string, unconfirmed bool) {
	t.Helper()
	if err := s.WriteAccount(t.Context(), &account.Account{Email: email, Username: username, Unconfirmed: unconfirmed}); err != nil {
		t.Fatalf("WriteAccount: %v", err)
	}
}

func TestRun_ReadBack(t *testing.T) {
	end := time.Date(2026, 1, 2, 6, 0, 0, 1, plus2())
	open := Run{ID: 1, Source: "cli", StartedAt: runStart().UTC(), Config: json.RawMessage(runConfigText)}
	ended := open
	ended.EndedAt = end.UTC()
	failed := ended
	failed.Error = "boom"
	tests := []struct {
		name   string
		end    bool
		runErr string
		want   Run
	}{
		{name: "open", want: open},
		{name: "ended", end: true, want: ended},
		{name: "ended with an error", end: true, runErr: "boom", want: failed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			id := mustWriteRun(t, s)
			if tt.end {
				if err := s.EndRun(t.Context(), id, end, tt.runErr); err != nil {
					t.Fatalf("EndRun: %v", err)
				}
			}
			got, err := s.Run(t.Context(), id)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Run = %+v, want %+v", got, tt.want)
			}
			var ended, runErr sql.NullString
			if err := s.db.QueryRowContext(t.Context(), "SELECT ended_at, error FROM runs").Scan(&ended, &runErr); err != nil {
				t.Fatal(err)
			}
			if ended.Valid != tt.end || runErr.Valid != (tt.runErr != "") {
				t.Errorf("ended_at, error = %v, %v, want NULL for each value that the run lacks", ended, runErr)
			}
		})
	}
}

func TestRun_NotFound(t *testing.T) {
	s := mustOpen(t)
	mustWriteRun(t, s)
	_, err := s.Run(t.Context(), 2)
	if !errors.Is(err, ErrNotFound) || err.Error() != "reading run 2: not found" {
		t.Errorf("Run error = %v, want reading run 2: not found", err)
	}
}

func TestWriteRun_BadConfig(t *testing.T) {
	tests := []struct {
		name   string
		config json.RawMessage
		want   string
	}{
		{name: "array", config: json.RawMessage(`[]`), want: "CHECK constraint failed"},
		{name: "empty", config: nil, want: "malformed JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			_, err := s.WriteRun(t.Context(), "serve", runStart(), tt.config)
			if err == nil || !strings.HasPrefix(err.Error(), "saving run: ") || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("WriteRun error = %v, want %q with the saving run prefix", err, tt.want)
			}
		})
	}
}

func TestWriteAttempt_Constraints(t *testing.T) {
	tests := []struct {
		name    string
		attempt Attempt
		email   string
		want    string
	}{
		{name: "missing run", attempt: Attempt{RunID: 2}, want: "FOREIGN KEY constraint failed"},
		{name: "category with no error", attempt: Attempt{RunID: 1, Category: "unknown"}, want: "CHECK constraint failed"},
		{name: "error with no category", attempt: Attempt{RunID: 1, Error: "boom"}, want: "CHECK constraint failed"},
		{name: "second row for one account", attempt: Attempt{RunID: 1}, email: "a@b.com", want: "UNIQUE constraint failed: attempts.account_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			mustWriteRun(t, s)
			mustWriteAccount(t, s, "a@b.com", "u", false)
			mustWriteAttempt(t, s, &Attempt{RunID: 1}, "a@b.com")
			err := s.WriteAttempt(t.Context(), &tt.attempt, tt.email)
			if err == nil || !strings.HasPrefix(err.Error(), "saving attempt: ") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("WriteAttempt error = %v, want %q with the saving attempt prefix", err, tt.want)
			}
			// A failed write does not stop a later one.
			mustWriteAttempt(t, s, &Attempt{RunID: 1, Category: "unknown", Error: "boom"}, "")
			attempts, _, err := s.Attempts(t.Context(), 1, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 2 {
				t.Errorf("run holds %d attempts, want 2", len(attempts))
			}
		})
	}
}

// storedAttempt is the text of the columns that the Attempt type maps to an empty field or a duration.
type storedAttempt struct {
	startedAt                                                string
	durationMS                                               int64
	proxy, category, errText, failedStep, failedPhase, steps sql.NullString
	accountID                                                sql.NullInt64
}

func TestWriteAttempt_RoundTrip(t *testing.T) {
	full := Attempt{
		RunID: 1, Worker: 3, StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456789, plus2()), Duration: 1500600 * time.Microsecond,
		Proxy: "1.2.3.4:8080", Category: "unknown", Error: "boom", FailedStep: "password", FailedPhase: "http",
		Steps:            []Step{{Name: "browser_slot_wait", Duration: 1499 * time.Microsecond}, {Name: "password", Duration: 1500 * time.Microsecond}},
		BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4,
	}
	fullWant := full
	fullWant.ID, fullWant.AccountID, fullWant.Email, fullWant.Username = 1, 1, "a@b.com", "u"
	fullWant.StartedAt, fullWant.Duration = full.StartedAt.UTC(), 1501*time.Millisecond
	fullWant.Steps = []Step{{Name: "browser_slot_wait", Duration: time.Millisecond}, {Name: "password", Duration: 2 * time.Millisecond}}
	text := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	tests := []struct {
		name       string
		attempt    Attempt
		email      string
		want       Attempt
		wantStored storedAttempt
	}{
		{
			name: "full", attempt: full, email: "a@b.com", want: fullWant,
			wantStored: storedAttempt{
				startedAt: "2026-01-02T01:04:05.123456789Z", durationMS: 1501, proxy: text("1.2.3.4:8080"),
				category: text("unknown"), errText: text("boom"), failedStep: text("password"), failedPhase: text("http"),
				steps:     text(`[{"name":"browser_slot_wait","duration_ms":1},{"name":"password","duration_ms":2}]`),
				accountID: sql.NullInt64{Int64: 1, Valid: true},
			},
		},
		{
			name: "minimal", attempt: Attempt{RunID: 1}, want: Attempt{ID: 1, RunID: 1, Steps: []Step{}},
			wantStored: storedAttempt{startedAt: "0001-01-01T00:00:00.000000000Z", steps: text("[]")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			mustWriteRun(t, s)
			mustWriteAccount(t, s, "a@b.com", "u", false)
			mustWriteAttempt(t, s, &tt.attempt, tt.email)
			got, _, err := s.Attempts(t.Context(), 1, 0, 10)
			if err != nil {
				t.Fatalf("Attempts: %v", err)
			}
			if want := []Attempt{tt.want}; !reflect.DeepEqual(got, want) {
				t.Errorf("Attempts = %+v, want %+v", got, want)
			}
			var st storedAttempt
			if err := s.db.QueryRowContext(t.Context(), `SELECT started_at, duration_ms, proxy, category, error,
				failed_step, failed_phase, steps, account_id FROM attempts`).Scan(&st.startedAt, &st.durationMS,
				&st.proxy, &st.category, &st.errText, &st.failedStep, &st.failedPhase, &st.steps, &st.accountID); err != nil {
				t.Fatal(err)
			}
			if st != tt.wantStored {
				t.Errorf("row = %+v, want %+v", st, tt.wantStored)
			}
		})
	}
}

func TestWriteAttempt_LinksAccountByEmail(t *testing.T) {
	tests := []struct {
		name                    string
		email                   string
		wantID                  int64
		wantEmail, wantUsername string
	}{
		{name: "saved account", email: "a@b.com", wantID: 2, wantEmail: "a@b.com", wantUsername: "ua"},
		// An account with an empty email exists, so a link by the empty string would find it.
		{name: "no account", email: ""},
		{name: "failed save", email: "missing@b.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			mustWriteRun(t, s)
			mustWriteAccount(t, s, "", "u0", false)
			mustWriteAccount(t, s, "a@b.com", "ua", false)
			mustWriteAttempt(t, s, &Attempt{RunID: 1}, tt.email)
			got, _, err := s.Attempts(t.Context(), 1, 0, 10)
			if err != nil {
				t.Fatalf("Attempts: %v", err)
			}
			if len(got) != 1 || got[0].AccountID != tt.wantID || got[0].Email != tt.wantEmail || got[0].Username != tt.wantUsername {
				t.Errorf("Attempts = %+v, want one attempt with account %d %q %q", got, tt.wantID, tt.wantEmail, tt.wantUsername)
			}
		})
	}
}

// collectPages reads every page through read, from the first page on, and returns the ids of each page.
func collectPages(t *testing.T, read func(before int64) (ids []int64, next int64, err error)) [][]int64 {
	t.Helper()
	var pages [][]int64
	var before int64
	for range 10 {
		ids, next, err := read(before)
		if err != nil {
			t.Fatalf("reading the page before %d: %v", before, err)
		}
		pages = append(pages, ids)
		if next == 0 {
			return pages
		}
		before = next
	}
	t.Fatalf("pages = %v, want a last page within 10 pages", pages)
	return nil
}

// pagingTest holds the pages at limit 2 of a table with rows rows.
type pagingTest struct {
	name string
	rows int
	want [][]int64
}

func pagingTests() []pagingTest {
	return []pagingTest{
		{name: "empty", rows: 0, want: [][]int64{{}}},
		{name: "exactly limit", rows: 2, want: [][]int64{{2, 1}}},
		{name: "limit plus one", rows: 3, want: [][]int64{{3, 2}, {1}}},
		{name: "two full pages", rows: 4, want: [][]int64{{4, 3}, {2, 1}}},
	}
}

func TestRuns_Paging(t *testing.T) {
	for _, tt := range pagingTests() {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			for range tt.rows {
				mustWriteRun(t, s)
			}
			got := collectPages(t, func(before int64) ([]int64, int64, error) {
				runs, next, err := s.Runs(t.Context(), before, 2)
				ids := make([]int64, 0, len(runs))
				for _, r := range runs {
					ids = append(ids, r.ID)
				}
				return ids, next, err
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pages = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAttempts_Paging(t *testing.T) {
	for _, tt := range pagingTests() {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			mustWriteRun(t, s)
			other := mustWriteRun(t, s)
			// Run 1 holds the odd attempt ids. The attempts of the other run interleave with them.
			for range tt.rows {
				mustWriteAttempt(t, s, &Attempt{RunID: 1}, "")
				mustWriteAttempt(t, s, &Attempt{RunID: other}, "")
			}
			got := collectPages(t, func(before int64) ([]int64, int64, error) {
				attempts, next, err := s.Attempts(t.Context(), 1, before, 2)
				ids := make([]int64, 0, len(attempts))
				for _, a := range attempts {
					ids = append(ids, (a.ID+1)/2)
				}
				return ids, next, err
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pages = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountRuns(t *testing.T) {
	s := mustOpen(t)
	mustWriteRun(t, s)
	mustWriteRun(t, s)
	mustWriteRun(t, s)
	mustWriteAccount(t, s, "a@b.com", "ua", false)
	mustWriteAccount(t, s, "c@b.com", "uc", true)
	failed := func(category string, n int64) *Attempt {
		return &Attempt{RunID: 1, Category: category, Error: "boom", BrowserBytesSent: n, BrowserBytesReceived: 2 * n, HTTPBytesSent: 3 * n, HTTPBytesReceived: 4 * n}
	}
	mustWriteAttempt(t, s, &Attempt{RunID: 1, BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4}, "a@b.com")
	mustWriteAttempt(t, s, failed("unknown", 10), "c@b.com")
	mustWriteAttempt(t, s, failed("proxy_error", 100), "")
	mustWriteAttempt(t, s, failed("proxy_error", 1000), "")
	mustWriteAttempt(t, s, failed("save_failed", 10000), "missing@b.com")
	// Run 3 is not asked for, so its attempt counts nowhere.
	mustWriteAttempt(t, s, &Attempt{RunID: 3, Category: "unknown", Error: "boom", BrowserBytesSent: 7}, "")
	got, err := s.CountRuns(t.Context(), []int64{1, 2})
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	want := map[int64]RunCounts{
		1: {
			Attempts: 5, Accounts: 2, Unconfirmed: 1,
			Failures:         map[string]int{"unknown": 1, "proxy_error": 2, "save_failed": 1},
			BrowserBytesSent: 11111, BrowserBytesReceived: 22222, HTTPBytesSent: 33333, HTTPBytesReceived: 44444,
		},
		2: {Failures: map[string]int{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CountRuns = %+v, want %+v", got, want)
	}
	none, err := s.CountRuns(t.Context(), nil)
	if err != nil || len(none) != 0 {
		t.Errorf("CountRuns of no runs = %v, %v, want an empty map", none, err)
	}
}

func TestCountRuns_QueryFails(t *testing.T) {
	tests := []struct {
		name  string
		match string
		want  string
	}{
		{name: "totals", match: "GROUP BY t.run_id", want: "reading run counts: "},
		{name: "failures", match: "GROUP BY run_id, category", want: "reading run failures: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := faultDB(t, &faultConnector{match: tt.match, fail: 1})
			if err := setUp(t.Context(), db, migrations, filepath.Join(t.TempDir(), "accounts.jsonl"), missingProxies(t), logtest.New(io.Discard)); err != nil {
				t.Fatal(err)
			}
			s := &Store{db: db}
			_, err := s.CountRuns(t.Context(), []int64{1})
			if !errors.Is(err, errFault) || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("CountRuns error = %v, want the fault with the prefix %q", err, tt.want)
			}
		})
	}
}

func TestReads_BadRows(t *testing.T) {
	const runRow = `INSERT INTO runs (source, started_at, ended_at, config) VALUES ('cli', '%s', %s, '{}')`
	const attemptRow = `INSERT INTO runs (source, started_at, config) VALUES ('cli', '` + timeLayout + `', '{}');
		INSERT INTO attempts (run_id, worker, started_at, duration_ms, steps) VALUES (1, 0, '%s', 0, '%s')`
	runs := func(ctx context.Context, s *Store) error { _, _, err := s.Runs(ctx, 0, 10); return err }
	attempts := func(ctx context.Context, s *Store) error { _, _, err := s.Attempts(ctx, 1, 0, 10); return err }
	tests := []struct {
		name  string
		setup string
		read  func(ctx context.Context, s *Store) error
		want  string
	}{
		{
			name: "NULL in a text column",
			setup: `DROP TABLE attempts; DROP TABLE runs; CREATE TABLE runs (id INTEGER PRIMARY KEY, source, started_at, ended_at, error, config);
				INSERT INTO runs (id) VALUES (1)`,
			read: runs, want: "reading runs row: ",
		},
		{
			// The abs of the smallest integer overflows on the second row, after the first row reached the caller.
			name: "error on a later row",
			setup: `DROP TABLE attempts; DROP TABLE runs; CREATE TABLE t (id INTEGER PRIMARY KEY); INSERT INTO t VALUES (1), (2);
				CREATE VIEW runs AS SELECT id, abs(-9223372036854775807 - id) AS source, '` + timeLayout + `' AS started_at,
				NULL AS ended_at, NULL AS error, '{}' AS config FROM t`,
			read: runs, want: "reading runs: ",
		},
		{name: "bad started_at of a run", setup: fmt.Sprintf(runRow, "yesterday", "NULL"), read: runs, want: "reading the times of run 1: "},
		{
			name: "bad ended_at of a run", setup: fmt.Sprintf(runRow, timeLayout, "'tomorrow'"),
			read: func(ctx context.Context, s *Store) error { _, err := s.Run(ctx, 1); return err },
			want: "reading the times of run 1: ",
		},
		{name: "bad started_at of an attempt", setup: fmt.Sprintf(attemptRow, "yesterday", "[]"), read: attempts, want: "reading started_at of attempt 1: "},
		{name: "bad steps", setup: fmt.Sprintf(attemptRow, timeLayout, "[1]"), read: attempts, want: "reading the steps of attempt 1: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			if _, err := s.db.ExecContext(t.Context(), tt.setup); err != nil {
				t.Fatalf("setup: %v", err)
			}
			if err := tt.read(t.Context(), s); err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("read error = %v, want the prefix %q", err, tt.want)
			}
		})
	}
}

func TestRuns_AfterClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(t.Context(), filepath.Join(dir, "creator.db"), filepath.Join(dir, "accounts.jsonl"), filepath.Join(dir, "proxies.txt"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := t.Context()
	calls := []struct {
		name string
		call func() error
		want string
	}{
		{name: "WriteRun", call: func() error { _, err := s.WriteRun(ctx, "cli", runStart(), json.RawMessage(runConfigText)); return err }, want: "saving run: "},
		{name: "EndRun", call: func() error { return s.EndRun(ctx, 1, runStart(), "") }, want: "saving the end of run 1: "},
		{name: "WriteAttempt", call: func() error { return s.WriteAttempt(ctx, &Attempt{RunID: 1}, "") }, want: "saving attempt: "},
		{name: "Run", call: func() error { _, err := s.Run(ctx, 1); return err }, want: "reading run: "},
		{name: "Runs", call: func() error { _, _, err := s.Runs(ctx, 0, 1); return err }, want: "reading runs: "},
		{name: "Attempts", call: func() error { _, _, err := s.Attempts(ctx, 1, 0, 1); return err }, want: "reading attempts: "},
		{name: "CountRuns", call: func() error { _, err := s.CountRuns(ctx, []int64{1}); return err }, want: "starting the counts transaction: "},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if want := c.want + "sql: database is closed"; err == nil || err.Error() != want {
				t.Errorf("%s error = %v, want %s", c.name, err, want)
			}
		})
	}
}

func TestIDArray(t *testing.T) {
	for _, tt := range []struct {
		ids  []int64
		want string
	}{
		{ids: nil, want: "[]"},
		{ids: []int64{1}, want: "[1]"},
		{ids: []int64{3, 12}, want: "[3,12]"},
	} {
		if got := idArray(tt.ids); got != tt.want {
			t.Errorf("idArray(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}
