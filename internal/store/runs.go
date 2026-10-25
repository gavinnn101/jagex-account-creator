package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound matches the error of a read or a write whose row does not exist.
var ErrNotFound = errors.New("not found")

// Run is one run of the full mode or of serve.
// Limitation: a process that dies leaves its run with a zero EndedAt, so the runs list shows it as unfinished.
type Run struct {
	ID        int64
	Source    string
	StartedAt time.Time
	EndedAt   time.Time       // zero while the run is in progress
	Error     string          // empty on a run that ended with no error
	Config    json.RawMessage // a JSON object
}

// Step is one timed step of an attempt.
type Step struct {
	Name     string
	Duration time.Duration
}

// Attempt is one attempt that ended. An empty text field is a field that the record line omits.
// ID and the account fields are read only: a read sets them, and WriteAttempt ignores them.
type Attempt struct {
	ID                   int64
	RunID                int64
	Worker               int
	StartedAt            time.Time
	Duration             time.Duration
	Proxy                string
	Category             string
	Error                string
	FailedStep           string
	FailedPhase          string
	Steps                []Step
	BrowserBytesSent     int64
	BrowserBytesReceived int64
	HTTPBytesSent        int64
	HTTPBytesReceived    int64

	// The saved account of the attempt, joined by a read. AccountID is 0 when the attempt saved no account.
	AccountID int64
	Email     string
	Username  string
}

// RunCounts holds the totals of one run, from its attempt rows. Unconfirmed reads
// accounts.unconfirmed of the linked accounts, so a confirm lowers it.
type RunCounts struct {
	Attempts             int
	Accounts             int            // the saved accounts, unconfirmed ones included
	Unconfirmed          int            // the linked accounts that are still unconfirmed
	Failures             map[string]int // the failed attempts by category
	BrowserBytesSent     int64
	BrowserBytesReceived int64
	HTTPBytesSent        int64
	HTTPBytesReceived    int64
}

// stepJSON is one element of the steps column.
type stepJSON struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
}

const insertRunSQL = `INSERT INTO runs (source, started_at, config) VALUES (?, ?, ?) RETURNING id`

const endRunSQL = `UPDATE runs SET ended_at = ?, error = ? WHERE id = ?`

const insertAttemptSQL = `INSERT INTO attempts (run_id, worker, account_id, started_at, duration_ms, proxy, category, error,
	failed_step, failed_phase, steps, browser_bytes_sent, browser_bytes_received, http_bytes_sent, http_bytes_received)
VALUES (?, ?, (SELECT id FROM accounts WHERE email = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// selectRunsFromSQL lists the columns in the order of runRow.dest.
const selectRunsFromSQL = `SELECT id, source, started_at, coalesce(ended_at, ''), coalesce(error, ''), config FROM runs`

const selectRunSQL = selectRunsFromSQL + ` WHERE id = ?`

// A before of 0 is the first page.
const selectRunsSQL = selectRunsFromSQL + ` WHERE ?1 = 0 OR id < ?1 ORDER BY id DESC LIMIT ?2`

// selectAttemptsSQL lists the columns in the order of attemptRow.dest.
const selectAttemptsSQL = `SELECT t.id, t.run_id, t.worker, t.started_at, t.duration_ms, coalesce(t.proxy, ''),
	coalesce(t.category, ''), coalesce(t.error, ''), coalesce(t.failed_step, ''), coalesce(t.failed_phase, ''), t.steps,
	t.browser_bytes_sent, t.browser_bytes_received, t.http_bytes_sent, t.http_bytes_received,
	coalesce(a.id, 0), coalesce(a.email, ''), coalesce(a.username, '')
FROM attempts t LEFT JOIN accounts a ON a.id = t.account_id
WHERE t.run_id = ?1 AND (?2 = 0 OR t.id < ?2) ORDER BY t.id DESC LIMIT ?3`

// countRunsSQL and countFailuresSQL take the run ids as one JSON array, so the statements stay static.
const countRunsSQL = `SELECT t.run_id, count(*), count(t.account_id), coalesce(sum(a.unconfirmed), 0),
	sum(t.browser_bytes_sent), sum(t.browser_bytes_received), sum(t.http_bytes_sent), sum(t.http_bytes_received)
FROM attempts t LEFT JOIN accounts a ON a.id = t.account_id
WHERE t.run_id IN (SELECT value FROM json_each(?))
GROUP BY t.run_id`

const countFailuresSQL = `SELECT run_id, category, count(*) FROM attempts
WHERE run_id IN (SELECT value FROM json_each(?)) AND category IS NOT NULL
GROUP BY run_id, category`

// WriteRun saves a new run of source that started at startedAt with config, a
// JSON object, and returns the id of the run.
func (s *Store) WriteRun(ctx context.Context, source string, startedAt time.Time, config json.RawMessage) (int64, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, insertRunSQL, source, startedAt.UTC().Format(timeLayout), string(config)).Scan(&id); err != nil {
		return 0, fmt.Errorf("saving run: %w", err)
	}
	return id, nil
}

// EndRun saves the end of the run id at endedAt with runErr, the error that
// the run returned. An empty runErr is a run that ended with no error.
func (s *Store) EndRun(ctx context.Context, id int64, endedAt time.Time, runErr string) error {
	if _, err := s.db.ExecContext(ctx, endRunSQL, endedAt.UTC().Format(timeLayout), nullIfEmpty(runErr), id); err != nil {
		return fmt.Errorf("saving the end of run %d: %w", id, err)
	}
	return nil
}

// WriteAttempt saves a as a new row of its run. It links the account with
// the address email, or no account when email is empty. Each call returns the
// error of its own row, as WriteAccount does.
// Limitation: a save_failed attempt has no account to link, so its row has no email and no username.
// The record line keeps them. It fails toward a row with less detail.
func (s *Store) WriteAttempt(ctx context.Context, a *Attempt, email string) error {
	steps := make([]stepJSON, len(a.Steps))
	for i, st := range a.Steps {
		steps[i] = stepJSON{Name: st.Name, DurationMS: milliseconds(st.Duration)}
	}
	text, err := encodeJSON(steps)
	if err == nil {
		_, err = s.db.ExecContext(ctx, insertAttemptSQL,
			a.RunID, a.Worker, nullIfEmpty(email), a.StartedAt.UTC().Format(timeLayout), milliseconds(a.Duration),
			nullIfEmpty(a.Proxy), nullIfEmpty(a.Category), nullIfEmpty(a.Error),
			nullIfEmpty(a.FailedStep), nullIfEmpty(a.FailedPhase), string(text),
			a.BrowserBytesSent, a.BrowserBytesReceived, a.HTTPBytesSent, a.HTTPBytesReceived)
	}
	if err != nil {
		return fmt.Errorf("saving attempt: %w", err)
	}
	return nil
}

// Run returns the run whose id is id. The error matches ErrNotFound when no run has that id.
func (s *Store) Run(ctx context.Context, id int64) (Run, error) {
	rows, err := queryRows(ctx, s.db, "run", (*runRow).dest, selectRunSQL, id)
	if err != nil {
		return Run{}, err
	}
	runs, err := convertRows(rows, (*runRow).run)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, fmt.Errorf("reading run %d: %w", id, ErrNotFound)
	}
	return runs[0], nil
}

// Runs returns at most limit runs with an id below before, newest first. A
// before of 0 is the first page. next is the before of the next page, or 0 on
// the last page. limit must be at least 1.
func (s *Store) Runs(ctx context.Context, before int64, limit int) (runs []Run, next int64, err error) {
	rows, err := queryRows(ctx, s.db, "runs", (*runRow).dest, selectRunsSQL, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	rows, next = cutPage(rows, limit, func(r *runRow) int64 { return r.r.ID })
	if runs, err = convertRows(rows, (*runRow).run); err != nil {
		return nil, 0, err
	}
	return runs, next, nil
}

// Attempts returns at most limit attempts of the run runID with an id below
// before, newest first, as Runs pages the runs.
func (s *Store) Attempts(ctx context.Context, runID, before int64, limit int) (attempts []Attempt, next int64, err error) {
	rows, err := queryRows(ctx, s.db, "attempts", (*attemptRow).dest, selectAttemptsSQL, runID, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	rows, next = cutPage(rows, limit, func(r *attemptRow) int64 { return r.a.ID })
	if attempts, err = convertRows(rows, (*attemptRow).attempt); err != nil {
		return nil, 0, err
	}
	return attempts, next, nil
}

// CountRuns returns the counts of each run in runIDs. A run with no attempts has zero counts.
// One read transaction holds both queries, so the totals and the failures count the same rows.
func (s *Store) CountRuns(ctx context.Context, runIDs []int64) (map[int64]RunCounts, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("starting the counts transaction: %w", err)
	}
	// The transaction only reads, so the rollback loses nothing.
	defer func() { _ = tx.Rollback() }()
	ids := idArray(runIDs)
	totals, err := queryRows(ctx, tx, "run counts", (*runTotals).dest, countRunsSQL, ids)
	if err != nil {
		return nil, err
	}
	failures, err := queryRows(ctx, tx, "run failures", (*runFailures).dest, countFailuresSQL, ids)
	if err != nil {
		return nil, err
	}
	counts := make(map[int64]RunCounts, len(runIDs))
	for _, id := range runIDs {
		counts[id] = RunCounts{Failures: map[string]int{}}
	}
	for _, t := range totals {
		t.counts.Failures = counts[t.runID].Failures
		counts[t.runID] = t.counts
	}
	for _, f := range failures {
		counts[f.runID].Failures[f.category] = f.count
	}
	return counts, nil
}

// runRow is one row of selectRunsFromSQL, with the times and the config as text.
type runRow struct {
	r                      Run
	started, ended, config string
}

func (x *runRow) dest() []any {
	return []any{&x.r.ID, &x.r.Source, &x.started, &x.ended, &x.r.Error, &x.config}
}

// run returns the run of x with its times parsed.
func (x *runRow) run() (Run, error) {
	r := x.r
	r.Config = json.RawMessage(x.config)
	var err error
	r.StartedAt, err = time.Parse(timeLayout, x.started)
	if err == nil && x.ended != "" {
		r.EndedAt, err = time.Parse(timeLayout, x.ended)
	}
	if err != nil {
		return r, fmt.Errorf("reading the times of run %d: %w", r.ID, err)
	}
	return r, nil
}

// attemptRow is one row of selectAttemptsSQL, with the start, the duration and the steps as stored.
type attemptRow struct {
	a              Attempt
	started, steps string
	durationMS     int64
}

func (x *attemptRow) dest() []any {
	a := &x.a
	return []any{&a.ID, &a.RunID, &a.Worker, &x.started, &x.durationMS, &a.Proxy,
		&a.Category, &a.Error, &a.FailedStep, &a.FailedPhase, &x.steps,
		&a.BrowserBytesSent, &a.BrowserBytesReceived, &a.HTTPBytesSent, &a.HTTPBytesReceived,
		&a.AccountID, &a.Email, &a.Username}
}

// attempt returns the attempt of x with its start and its steps parsed.
func (x *attemptRow) attempt() (Attempt, error) {
	a := x.a
	a.Duration = time.Duration(x.durationMS) * time.Millisecond
	var err error
	if a.StartedAt, err = time.Parse(timeLayout, x.started); err != nil {
		return a, fmt.Errorf("reading started_at of attempt %d: %w", a.ID, err)
	}
	var steps []stepJSON
	if err := json.Unmarshal([]byte(x.steps), &steps); err != nil {
		return a, fmt.Errorf("reading the steps of attempt %d: %w", a.ID, err)
	}
	a.Steps = make([]Step, len(steps))
	for i, st := range steps {
		a.Steps[i] = Step{Name: st.Name, Duration: time.Duration(st.DurationMS) * time.Millisecond}
	}
	return a, nil
}

// runTotals is one row of countRunsSQL.
type runTotals struct {
	runID  int64
	counts RunCounts
}

func (t *runTotals) dest() []any {
	c := &t.counts
	return []any{&t.runID, &c.Attempts, &c.Accounts, &c.Unconfirmed,
		&c.BrowserBytesSent, &c.BrowserBytesReceived, &c.HTTPBytesSent, &c.HTTPBytesReceived}
}

// runFailures is one row of countFailuresSQL.
type runFailures struct {
	runID    int64
	category string
	count    int
}

func (f *runFailures) dest() []any {
	return []any{&f.runID, &f.category, &f.count}
}

// queryRows runs query with args on q and reads each row into the fields that
// dest returns of a new T. what names the rows in an error.
// Limitation: a read holds the one connection, so a write waits for it. It fails toward a delayed write, never a lost one.
func queryRows[T any](ctx context.Context, q dbtx, what string, dest func(*T) []any, query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		var v T
		if err := rows.Scan(dest(&v)...); err != nil {
			return nil, fmt.Errorf("reading %s row: %w", what, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", what, err)
	}
	return out, nil
}

// convertRows returns conv of each row. The result is never nil, so an empty page is an empty list.
func convertRows[R, T any](rows []R, conv func(*R) (T, error)) ([]T, error) {
	out := make([]T, len(rows))
	for i := range rows {
		v, err := conv(&rows[i])
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// cutPage trims rows, read with a limit of limit+1, to at most limit rows.
// next is the id of the last row kept when a row was cut, else 0.
func cutPage[T any](rows []T, limit int, id func(*T) int64) (page []T, next int64) {
	if len(rows) <= limit {
		return rows, 0
	}
	page = rows[:limit]
	return page, id(&page[limit-1])
}

// idArray returns ids as one JSON array of numbers.
func idArray(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// nullIfEmpty stores s as TEXT, or as NULL when s is empty.
func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// milliseconds rounds d to the millisecond, as the record line does.
func milliseconds(d time.Duration) int64 {
	return d.Round(time.Millisecond).Milliseconds()
}
