package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"modernc.org/sqlite"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/logtest"
)

// guard bounds an Open, so a set-up that waits for the one connection fails the test.
const guard = 2 * time.Second

// openGuarded runs Open under the guard, with the proxy file proxies.txt beside
// the database. Cleanup closes a store that Open returns.
func openGuarded(ctx context.Context, t *testing.T, dbPath, importPath string, logger *slog.Logger) (*Store, error) {
	t.Helper()
	type result struct {
		s   *Store
		err error
	}
	proxiesPath := filepath.Join(filepath.Dir(dbPath), "proxies.txt")
	done := make(chan result, 1)
	go func() {
		s, err := Open(ctx, dbPath, importPath, proxiesPath, logger)
		done <- result{s, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(guard):
		t.Fatalf("Open did not return within %v", guard)
	}
	if r.s != nil {
		t.Cleanup(func() {
			if err := r.s.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
	return r.s, r.err
}

// mustOpen opens a new store in a temp dir with no JSONL file beside it.
func mustOpen(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := openGuarded(t.Context(), t, filepath.Join(dir, "creator.db"), filepath.Join(dir, "accounts.jsonl"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// missingProxies returns the path of a proxy file that does not exist.
func missingProxies(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "proxies.txt")
}

// testDB opens a new database in a temp dir with no migration run.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "creator.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func dbVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	return version
}

// latestVersion returns the number of the last embedded migration, the version of a database that Open set up.
func latestVersion(t *testing.T) int {
	t.Helper()
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil || len(entries) == 0 {
		t.Fatalf("listing migrations = %d entries, %v", len(entries), err)
	}
	prefix, _, _ := strings.Cut(entries[len(entries)-1].Name(), "_")
	n, err := strconv.Atoi(prefix)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpen_NewDatabase(t *testing.T) {
	s := mustOpen(t)
	if got, want := dbVersion(t, s.db), latestVersion(t); got != want {
		t.Errorf("user_version = %d, want %d", got, want)
	}
	var mode string
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestOpen_Errors(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		canceled bool
		want     string
		wantIs   error
	}{
		{name: "bad DSN", file: "creator.db?%zz", want: "opening database "},
		{name: "missing dir", file: filepath.Join("missing", "creator.db"), want: "creating database file: ", wantIs: fs.ErrNotExist},
		{name: "canceled", file: "creator.db", canceled: true, want: "setting up database ", wantIs: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			dir := t.TempDir()
			path := filepath.Join(dir, tt.file)
			s, err := openGuarded(ctx, t, path, filepath.Join(dir, "accounts.jsonl"), logtest.New(io.Discard))
			if s != nil || err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("Open = %v, %v, want an error with the prefix %q", s, err, tt.want)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("error = %v, want %v", err, tt.wantIs)
			}
		})
	}
}

func TestOpen_BadDSNCreatesNoFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := openGuarded(t.Context(), t, filepath.Join(dir, "creator.db?%zz"), filepath.Join(dir, "accounts.jsonl"), logtest.New(io.Discard)); err == nil {
		t.Fatal("Open passed, want the DSN error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dir holds %d entries, want none", len(entries))
	}
}

func TestOpen_FileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file takes the ACL of its dir")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "creator.db")
	s, err := openGuarded(t.Context(), t, path, filepath.Join(dir, "accounts.jsonl"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.WriteAccount(t.Context(), &account.Account{Email: "a@b.com"}); err != nil {
		t.Fatalf("WriteAccount: %v", err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
		}
	}
}

func TestSetUp_MigrationErrors(t *testing.T) {
	file := func(script string) fstest.MapFS {
		return fstest.MapFS{"migrations/0001_a.sql": {Data: []byte(script)}}
	}
	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{name: "no migrations dir", files: fstest.MapFS{}, want: "listing migrations: "},
		{name: "no number prefix", files: fstest.MapFS{"migrations/a.sql": {}}, want: "numbering migration a.sql: "},
		{name: "unreadable file", files: fstest.MapFS{"migrations/0001_a.sql/x": {}}, want: "reading migration: "},
		{name: "bad SQL", files: file("CREATE TABL t (x);"), want: "running migration 0001_a.sql: "},
		{name: "wrong version", files: file("CREATE TABLE t (x); PRAGMA user_version = 2;"), want: "migration 0001_a.sql set the schema version to 2, want 1"},
		{name: "no version", files: file("CREATE TABLE t (x);"), want: "migration 0001_a.sql set the schema version to 0, want 1"},
		{name: "commit fails", files: file("CREATE TABLE t (x); PRAGMA user_version = 1; COMMIT;"), want: "committing the set-up transaction: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testDB(t)
			err := setUp(t.Context(), db, tt.files, filepath.Join(t.TempDir(), "accounts.jsonl"), missingProxies(t), logtest.New(io.Discard))
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("setUp error = %v, want the prefix %q", err, tt.want)
			}
		})
	}
}

func TestSetUp_RunsPendingFilesInOrder(t *testing.T) {
	files := fstest.MapFS{
		"migrations/0002_b.sql": {Data: []byte("INSERT INTO ran VALUES (2); PRAGMA user_version = 2;")},
		"migrations/0001_a.sql": {Data: []byte("CREATE TABLE ran (n INTEGER); INSERT INTO ran VALUES (1); PRAGMA user_version = 1;")},
	}
	db := testDB(t)
	missing := filepath.Join(t.TempDir(), "accounts.jsonl")
	if err := setUp(t.Context(), db, files, missing, missingProxies(t), logtest.New(io.Discard)); err != nil {
		t.Fatalf("first setUp: %v", err)
	}
	files["migrations/0003_c.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO ran VALUES (3); PRAGMA user_version = 3;")}
	if err := setUp(t.Context(), db, files, missing, missingProxies(t), logtest.New(io.Discard)); err != nil {
		t.Fatalf("second setUp: %v", err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT n FROM ran ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ran []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		ran = append(ran, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 3}; !slices.Equal(ran, want) {
		t.Errorf("ran = %v, want %v", ran, want)
	}
	if got := dbVersion(t, db); got != 3 {
		t.Errorf("user_version = %d, want 3", got)
	}
}

func TestSetUp_FailureChangesNothing(t *testing.T) {
	files := fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("CREATE TABLE t (x); PRAGMA user_version = 1;")},
		"migrations/0002_b.sql": {Data: []byte("CREATE TABL u (x);")},
	}
	db := testDB(t)
	if err := setUp(t.Context(), db, files, filepath.Join(t.TempDir(), "accounts.jsonl"), missingProxies(t), logtest.New(io.Discard)); err == nil {
		t.Fatal("setUp passed, want the error of 0002_b.sql")
	}
	if got := dbVersion(t, db); got != 0 {
		t.Errorf("user_version = %d, want 0", got)
	}
	var tables int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("schema holds %d entries, want none", tables)
	}
}

// A stored config of version 4 holds one imap_domain. Version 5 holds it as the one item of imap_domains.
func TestOpen_Version4MovesDomainIntoList(t *testing.T) {
	dbPath, _ := migratedDB(t, 4)
	const oldConfig = `{"database":"creator.db","email":{"imap_host":"mail.example.com","imap_domain":"example.com"}}`
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO runs (source, started_at, config) VALUES ('cli', '2025-01-15T10:30:00.000000000Z', ?)`, oldConfig)
	if err == nil {
		_, err = db.ExecContext(t.Context(), `INSERT INTO presets (name, config, saved_at) VALUES ('old', ?, '2025-01-15T10:30:00.000000000Z')`, oldConfig)
	}
	// Close releases the file and the WAL, so Windows lets the next Open use them.
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}

	s, err := openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	run, err := s.Run(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	presets, err := s.Presets(t.Context())
	if err != nil || len(presets) != 1 {
		t.Fatalf("Presets = %+v, %v, want one preset", presets, err)
	}
	for name, config := range map[string]json.RawMessage{"run": run.Config, "preset": presets[0].Config} {
		var got struct {
			Database string         `json:"database"`
			Email    map[string]any `json:"email"`
		}
		if err := json.Unmarshal(config, &got); err != nil {
			t.Fatalf("%s config %s: %v", name, config, err)
		}
		want := map[string]any{"imap_host": "mail.example.com", "imap_domains": []any{"example.com"}}
		if got.Database != "creator.db" || !reflect.DeepEqual(got.Email, want) {
			t.Errorf("%s config = %s, want the database and the email section %v", name, config, want)
		}
	}
}

var errFault = errors.New("fault")

// realConn is the method set of a SQLite connection that faultConn passes on.
type realConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ExecerContext
	driver.QueryerContext
}

// faultConnector opens real connections. Query number fail among the queries
// that contain match fails with errFault. Each Close closes the real
// connection, then returns closeErr.
type faultConnector struct {
	driver.Connector

	match    string
	fail     int32
	closeErr error
	seen     atomic.Int32
}

func (c *faultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	rc, ok := conn.(realConn)
	if !ok {
		return nil, errors.New("the SQLite connection lacks a context method")
	}
	return faultConn{realConn: rc, c: c}, nil
}

type faultConn struct {
	realConn

	c *faultConnector
}

func (f faultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, f.c.match) && f.c.seen.Add(1) == f.c.fail {
		return nil, errFault
	}
	rows, err := f.realConn.QueryContext(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("querying: %w", err)
	}
	return rows, nil
}

func (f faultConn) Close() error {
	return errors.Join(f.realConn.Close(), f.c.closeErr)
}

// faultDB opens a database in a temp dir through c. c.Connector is set here.
func faultDB(t *testing.T, c *faultConnector) *sql.DB {
	t.Helper()
	base, err := sqlite.NewConnector(filepath.Join(t.TempDir(), "creator.db") + dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	c.Connector = base
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSetUp_VersionReadFails(t *testing.T) {
	tests := []struct {
		name string
		fail int32
	}{
		{name: "before the migrations", fail: 1},
		{name: "after a migration", fail: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := faultDB(t, &faultConnector{match: "PRAGMA user_version", fail: tt.fail})
			err := setUp(t.Context(), db, migrations, filepath.Join(t.TempDir(), "accounts.jsonl"), missingProxies(t), logtest.New(io.Discard))
			if !errors.Is(err, errFault) || !strings.HasPrefix(err.Error(), "reading the schema version: ") {
				t.Errorf("setUp error = %v, want the fault with the reading the schema version prefix", err)
			}
		})
	}
}

func TestClose(t *testing.T) {
	t.Run("returns the close error", func(t *testing.T) {
		db := faultDB(t, &faultConnector{closeErr: errFault})
		if err := db.PingContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		s := &Store{db: db}
		err := s.Close()
		if !errors.Is(err, errFault) || !strings.HasPrefix(err.Error(), "closing database: ") {
			t.Errorf("Close error = %v, want the fault with the closing database prefix", err)
		}
	})

	t.Run("a second Close passes", func(t *testing.T) {
		s := mustOpen(t)
		if err := s.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
}
