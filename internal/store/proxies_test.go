package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/proxy"
)

// mustImport imports text into s and fails the test on an error.
func mustImport(t *testing.T, s *Store, text string) ProxyImport {
	t.Helper()
	res, err := s.ImportProxies(t.Context(), text)
	if err != nil {
		t.Fatalf("ImportProxies: %v", err)
	}
	return res
}

// mustProxies returns the proxies of s.
func mustProxies(t *testing.T, s *Store) []StoredProxy {
	t.Helper()
	got, err := s.Proxies(t.Context())
	if err != nil {
		t.Fatalf("Proxies: %v", err)
	}
	return got
}

// proxyLines returns each proxy of s as its id, its line and its enabled state, in id order.
func proxyLines(t *testing.T, s *Store) []string {
	t.Helper()
	proxies := mustProxies(t, s)
	lines := make([]string, 0, len(proxies))
	for _, p := range proxies {
		lines = append(lines, fmt.Sprintf("%d %s %v", p.ID, p.Proxy.Line(), p.Enabled))
	}
	return lines
}

func TestImportProxies(t *testing.T) {
	s := mustOpen(t)
	before := time.Now()
	if got, want := mustImport(t, s, "a:1:u1:p1\nsocks5://b:2\nc:3:u3:p3\n"), (ProxyImport{Added: 3}); got != want {
		t.Errorf("first import = %+v, want %+v", got, want)
	}
	after := time.Now()
	if err := s.SetProxyEnabled(t.Context(), 3, false); err != nil {
		t.Fatal(err)
	}

	// c:3 gets new credentials and stays disabled. b:2 over http is a new proxy beside socks5://b:2.
	// The last line of a:1 holds the stored credentials, so both lines of a:1 count as repeated.
	got := mustImport(t, s, "c:3:u9:p9\nb:2\nsocks5://b:2\nd:4\na:1:x:y\na:1:u1:p1\n")
	if want := (ProxyImport{Added: 2, Updated: 1, Repeated: 3}); got != want {
		t.Errorf("second import = %+v, want %+v", got, want)
	}
	want := []string{
		"1 a:1:u1:p1 true",
		"2 socks5://b:2 true",
		"3 c:3:u9:p9 false",
		"4 b:2 true",
		"5 d:4 true",
	}
	if got := proxyLines(t, s); !slices.Equal(got, want) {
		t.Errorf("proxies = %+v, want %+v", got, want)
	}
	for _, p := range mustProxies(t, s)[:3] {
		if p.AddedAt.Before(before.Truncate(time.Microsecond)) || p.AddedAt.After(after) || p.AddedAt.Location() != time.UTC {
			t.Errorf("proxy %d added_at = %v, want a UTC time from %v to %v", p.ID, p.AddedAt, before, after)
		}
	}

	enabled, err := s.EnabledProxies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantEnabled := []proxy.Proxy{
		{Scheme: "http", Host: "a", Port: "1", Username: "u1", Password: "p1"},
		{Scheme: "socks5", Host: "b", Port: "2"},
		{Scheme: "http", Host: "b", Port: "2"},
		{Scheme: "http", Host: "d", Port: "4"},
	}
	if !slices.Equal(enabled, wantEnabled) {
		t.Errorf("EnabledProxies = %+v, want %+v", enabled, wantEnabled)
	}
}

func TestImportProxies_BadLineChangesNothing(t *testing.T) {
	s := mustOpen(t)
	mustImport(t, s, "a:1:u:p\n")
	res, err := s.ImportProxies(t.Context(), "a:1:u2:p2\nb:2\nc:3:secret\n")
	lineErr, ok := errors.AsType[*proxy.LineError](err)
	if !ok || lineErr.Line != 3 || res != (ProxyImport{}) {
		t.Fatalf("ImportProxies = %+v, %v; want a *proxy.LineError of line 3", res, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error %q holds the line text", err)
	}
	if got, want := proxyLines(t, s), []string{"1 a:1:u:p true"}; !slices.Equal(got, want) {
		t.Errorf("proxies = %+v, want %+v", got, want)
	}
}

// seedAttempts saves one run and one attempt for each of attempts, in order.
func seedAttempts(t *testing.T, s *Store, attempts []Attempt) {
	t.Helper()
	runID, err := s.WriteRun(t.Context(), "cli", historyTime(0), []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range attempts {
		a := attempts[i]
		a.RunID = runID
		if a.Category != "" {
			a.Error = "failed"
		}
		if err := s.WriteAttempt(t.Context(), &a, ""); err != nil {
			t.Fatal(err)
		}
	}
}

// historyTime returns a fixed UTC time plus m minutes.
func historyTime(m int) time.Time {
	return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
}

func TestProxies_Health(t *testing.T) {
	s := mustOpen(t)
	mustImport(t, s, "a:1:u:p\nsocks5://b:2\nc:3\n")
	seedAttempts(t, s, []Attempt{
		{StartedAt: historyTime(0), Proxy: "a:1", BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4},
		{StartedAt: historyTime(1), Proxy: "a:1", Category: "proxy_error", BrowserBytesSent: 10, BrowserBytesReceived: 20, HTTPBytesSent: 30, HTTPBytesReceived: 40},
		{StartedAt: historyTime(2), Proxy: "a:1", Category: "rate_limited"},
		{StartedAt: historyTime(4), Proxy: "a:1"},
		{StartedAt: historyTime(3), Proxy: "a:1", BrowserBytesSent: 100},
		{StartedAt: historyTime(5), Proxy: "socks5://b:2", Category: "unknown"},
		// Neither attempt has a proxy row: b:2 over http is another proxy, and a direct attempt has none.
		{StartedAt: historyTime(6), Proxy: "b:2", Category: "proxy_error"},
		{StartedAt: historyTime(7)},
	})

	got := mustProxies(t, s)
	want := []ProxyHealth{
		{
			Attempts: 5, Successes: 3, BrowserBytesSent: 111, BrowserBytesReceived: 22, HTTPBytesSent: 33, HTTPBytesReceived: 44,
			LastAttemptAt: historyTime(4), LastFailure: "rate_limited",
		},
		{Attempts: 1, LastAttemptAt: historyTime(5), LastFailure: "unknown"},
		{},
	}
	if len(got) != len(want) {
		t.Fatalf("Proxies = %+v, want %d proxies", got, len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i].Health, want[i]) {
			t.Errorf("proxy %s health = %+v, want %+v", got[i].Proxy.HostPort(), got[i].Health, want[i])
		}
	}
}

func TestProxy(t *testing.T) {
	s := mustOpen(t)
	mustImport(t, s, "a:1:u:p\nsocks5://b:2\n")
	seedAttempts(t, s, []Attempt{
		{StartedAt: historyTime(0), Proxy: "a:1", BrowserBytesSent: 5},
		{StartedAt: historyTime(1), Proxy: "socks5://b:2", Category: "proxy_error"},
	})
	if err := s.SetProxyEnabled(t.Context(), 2, false); err != nil {
		t.Fatal(err)
	}
	all := mustProxies(t, s)
	for _, want := range all {
		got, err := s.Proxy(t.Context(), want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Proxy(%d) = %+v, %v; want %+v, the item of the list", want.ID, got, err, want)
		}
	}
	if all[1].Enabled || all[1].Proxy.HostPort() != "socks5://b:2" || all[1].Health.LastFailure != "proxy_error" {
		t.Errorf("proxy 2 = %+v, want disabled socks5://b:2 with its failure", all[1])
	}
	_, err := s.Proxy(t.Context(), 3)
	if !errors.Is(err, ErrNotFound) || err.Error() != "reading proxy 3: not found" {
		t.Errorf("Proxy of an unknown id = %v, want reading proxy 3: not found", err)
	}
}

func TestSetProxyEnabled(t *testing.T) {
	s := mustOpen(t)
	mustImport(t, s, "a:1\nb:2\n")
	for _, step := range []struct {
		id      int64
		enabled bool
		want    []string
	}{
		{1, false, []string{"1 a:1 false", "2 b:2 true"}},
		{1, false, []string{"1 a:1 false", "2 b:2 true"}},
		{2, false, []string{"1 a:1 false", "2 b:2 false"}},
		{1, true, []string{"1 a:1 true", "2 b:2 false"}},
	} {
		if err := s.SetProxyEnabled(t.Context(), step.id, step.enabled); err != nil {
			t.Fatalf("SetProxyEnabled(%d, %v): %v", step.id, step.enabled, err)
		}
		if got := proxyLines(t, s); !slices.Equal(got, step.want) {
			t.Errorf("after SetProxyEnabled(%d, %v): proxies = %+v, want %+v", step.id, step.enabled, got, step.want)
		}
	}
	err := s.SetProxyEnabled(t.Context(), 3, true)
	if !errors.Is(err, ErrNotFound) || err.Error() != "setting enabled of proxy 3: not found" {
		t.Errorf("SetProxyEnabled of an unknown id = %v, want setting enabled of proxy 3: not found", err)
	}
}

func TestDeleteProxy(t *testing.T) {
	s := mustOpen(t)
	mustImport(t, s, "a:1:u:p\nb:2\n")
	seedAttempts(t, s, []Attempt{{StartedAt: historyTime(0), Proxy: "a:1"}})
	if err := s.DeleteProxy(t.Context(), 1); err != nil {
		t.Fatalf("DeleteProxy: %v", err)
	}
	if got, want := proxyLines(t, s), []string{"2 b:2 true"}; !slices.Equal(got, want) {
		t.Errorf("proxies = %+v, want %+v", got, want)
	}
	err := s.DeleteProxy(t.Context(), 1)
	if !errors.Is(err, ErrNotFound) || err.Error() != "deleting proxy 1: not found" {
		t.Errorf("a second DeleteProxy = %v, want deleting proxy 1: not found", err)
	}

	// The attempt rows stay, so an import of the same HostPort links them again.
	if got := mustImport(t, s, "a:1:u:p\n"); got != (ProxyImport{Added: 1}) {
		t.Errorf("import = %+v, want one added", got)
	}
	got := mustProxies(t, s)
	if len(got) != 2 || got[1].ID != 3 || got[1].Health.Attempts != 1 || got[1].Health.Successes != 1 {
		t.Errorf("proxies = %+v, want a:1 as id 3 with its one attempt", got)
	}
}

func TestProxies_Errors(t *testing.T) {
	const badHost = `INSERT INTO proxies (host_port, added_at) VALUES ('bad host:1', '` + timeLayout + `')`
	list := func(s *Store) error { _, err := s.Proxies(t.Context()); return err }
	one := func(s *Store) error { _, err := s.Proxy(t.Context(), 1); return err }
	enabled := func(s *Store) error { _, err := s.EnabledProxies(t.Context()); return err }
	importA := func(s *Store) error { _, err := s.ImportProxies(t.Context(), "a:1:u2:p2\n"); return err }
	tests := []struct {
		name  string
		setup string
		call  func(s *Store) error
		want  string
	}{
		{name: "a bad host_port in the list", setup: badHost, call: list, want: "reading proxy bad host:1: host must be "},
		{name: "a bad host_port in the enabled list", setup: badHost, call: enabled, want: "reading proxy bad host:1: host must be "},
		{
			name:  "a bad added_at",
			setup: `INSERT INTO proxies (host_port, added_at) VALUES ('a:1', 'x')`,
			call:  list, want: "reading the times of proxy 1: ",
		},
		{
			name:  "a bad added_at of one proxy",
			setup: `INSERT INTO proxies (host_port, added_at) VALUES ('a:1', 'x')`,
			call:  one, want: "reading the times of proxy 1: ",
		},
		{
			name: "a bad attempt start",
			setup: `INSERT INTO proxies (host_port, added_at) VALUES ('a:1', '` + timeLayout + `');
				INSERT INTO runs (source, started_at, config) VALUES ('cli', 'x', '{}');
				INSERT INTO attempts (run_id, worker, started_at, duration_ms, proxy, steps) VALUES (1, 0, 'x', 0, 'a:1', '[]')`,
			call: list, want: "reading the times of proxy 1: ",
		},
		{
			name:  "a failed insert",
			setup: `CREATE TRIGGER fail BEFORE INSERT ON proxies BEGIN SELECT RAISE(FAIL, 'fault'); END`,
			call:  importA, want: "saving proxy a:1: ",
		},
		{
			name: "a failed update",
			setup: `INSERT INTO proxies (host_port, added_at) VALUES ('a:1', '` + timeLayout + `');
				CREATE TRIGGER fail BEFORE UPDATE ON proxies BEGIN SELECT RAISE(FAIL, 'fault'); END`,
			call: importA, want: "saving proxy a:1: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			if _, err := s.db.ExecContext(t.Context(), tt.setup); err != nil {
				t.Fatalf("setup: %v", err)
			}
			if err := tt.call(s); err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("error = %v, want the prefix %q", err, tt.want)
			}
		})
	}
}

func TestProxies_ClosedStore(t *testing.T) {
	s := mustOpen(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, tt := range []struct {
		name string
		call func() error
		want string
	}{
		{"Proxies", func() error { _, err := s.Proxies(ctx); return err }, "reading proxies: "},
		{"Proxy", func() error { _, err := s.Proxy(ctx, 1); return err }, "reading proxy: "},
		{"EnabledProxies", func() error { _, err := s.EnabledProxies(ctx); return err }, "reading enabled proxies: "},
		{"ImportProxies", func() error { _, err := s.ImportProxies(ctx, "a:1\n"); return err }, "starting the proxy import transaction: "},
		{"SetProxyEnabled", func() error { return s.SetProxyEnabled(ctx, 1, true) }, "setting enabled of proxy 1: "},
		{"DeleteProxy", func() error { return s.DeleteProxy(ctx, 1) }, "deleting proxy 1: "},
	} {
		if err := tt.call(); err == nil || !strings.HasPrefix(err.Error(), tt.want) || errors.Is(err, ErrNotFound) {
			t.Errorf("%s error = %v, want the prefix %q", tt.name, err, tt.want)
		}
	}
}

func TestImportProxies_ReadFails(t *testing.T) {
	db := faultDB(t, &faultConnector{match: selectProxyCredentialsSQL, fail: 1})
	if err := setUp(t.Context(), db, migrations, filepath.Join(t.TempDir(), "accounts.jsonl"), missingProxies(t), logtest.New(io.Discard)); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db}
	_, err := s.ImportProxies(t.Context(), "a:1\n")
	if !errors.Is(err, errFault) || !strings.HasPrefix(err.Error(), "reading proxies: ") {
		t.Errorf("ImportProxies error = %v, want the fault with the prefix %q", err, "reading proxies: ")
	}
}

// proxiesImportLog returns the log line of an import of count proxies from path.
func proxiesImportLog(count int, path string) string {
	var buf bytes.Buffer
	logtest.New(&buf).Info("imported proxies", "count", count, "path", path)
	return buf.String()
}

// version2DB creates a database at the schema version 2 in a temp dir, with
// one account, and returns the paths of the database and of its proxy file.
func version2DB(t *testing.T) (dbPath, proxiesPath string) {
	t.Helper()
	return migratedDB(t, 2)
}

// migratedDB creates a database in a temp dir with the embedded migrations up
// to version, and one account. It returns the paths of the database and of its
// proxy file, which does not exist.
func migratedDB(t *testing.T, version int) (dbPath, proxiesPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath, proxiesPath = filepath.Join(dir, "creator.db"), filepath.Join(dir, "proxies.txt")
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, entry := range entries[:version] {
		data, err := fs.ReadFile(migrations, "migrations/"+entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		files["migrations/"+entry.Name()] = &fstest.MapFile{Data: data}
	}
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	err = setUp(t.Context(), db, files, filepath.Join(dir, "accounts.jsonl"), proxiesPath, logtest.New(io.Discard))
	if err == nil {
		_, err = db.ExecContext(t.Context(), `INSERT INTO accounts (email, username, password, created_at) VALUES ('a@b.com', 'u', 'p', 'x')`)
	}
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	return dbPath, proxiesPath
}

// newDBPaths returns the paths of a new database and of its proxy file in a temp dir.
func newDBPaths(t *testing.T) (dbPath, proxiesPath string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "creator.db"), filepath.Join(dir, "proxies.txt")
}

func TestOpen_ImportsProxyFile(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) (dbPath, proxiesPath string)
	}{
		{name: "a new database", path: newDBPaths},
		{name: "a database at version 2", path: version2DB},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath, proxiesPath := tt.path(t)
			writeFile(t, proxiesPath, "# list\na:1:u:p\r\nsocks5h://b:2\na:1:u:p\n")
			var log bytes.Buffer
			s, err := openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(&log))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if got, want := dbVersion(t, s.db), latestVersion(t); got != want {
				t.Errorf("user_version = %d, want %d", got, want)
			}
			if want := proxiesImportLog(2, proxiesPath); log.String() != want {
				t.Errorf("log = %q, want %q", log.String(), want)
			}
			if got, want := proxyLines(t, s), []string{"1 a:1:u:p true", "2 socks5://b:2 true"}; !slices.Equal(got, want) {
				t.Errorf("proxies = %+v, want %+v", got, want)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// A database at the latest version never reads the file again.
			writeFile(t, proxiesPath, "c:3\n")
			log.Reset()
			s, err = openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(&log))
			if err != nil {
				t.Fatalf("second Open: %v", err)
			}
			if log.Len() != 0 || len(mustProxies(t, s)) != 2 {
				t.Errorf("second Open: log = %q, proxies = %+v; want no log and the two proxies", log.String(), mustProxies(t, s))
			}
		})
	}
}

func TestOpen_ProxyFileStates(t *testing.T) {
	t.Run("no proxy and no file log nothing", func(t *testing.T) {
		for _, content := range []*string{nil, new("# only a comment\n")} {
			dbPath, proxiesPath := version2DB(t)
			if content != nil {
				writeFile(t, proxiesPath, *content)
			}
			var log bytes.Buffer
			s, err := openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(&log))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if want := latestVersion(t); log.Len() != 0 || len(mustProxies(t, s)) != 0 || dbVersion(t, s.db) != want {
				t.Errorf("log = %q, proxies = %+v, version %d; want no log, no proxy and version %d", log.String(), mustProxies(t, s), dbVersion(t, s.db), want)
			}
		}
	})

	t.Run("a dir fails the read", func(t *testing.T) {
		dbPath, proxiesPath := version2DB(t)
		if err := os.Mkdir(proxiesPath, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(io.Discard))
		if want := fmt.Sprintf("setting up database %s: importing proxies: ", dbPath); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Open error = %v, want the prefix %q", err, want)
		}
	})

	t.Run("a bad line fails the start and changes nothing", func(t *testing.T) {
		dbPath, proxiesPath := version2DB(t)
		writeFile(t, proxiesPath, "a:1\nb:2:secret\n")
		_, err := openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(io.Discard))
		want := fmt.Sprintf("setting up database %s: importing %s: line 2: expected host:port or host:port:user:pass, got 3 parts", dbPath, proxiesPath)
		if err == nil || err.Error() != want {
			t.Fatalf("Open error = %v, want %q", err, want)
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := dbVersion(t, db); got != 2 {
			t.Errorf("user_version after the failed start = %d, want 2", got)
		}

		writeFile(t, proxiesPath, "a:1\n")
		var log bytes.Buffer
		if _, err := openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(&log)); err != nil {
			t.Fatalf("Open after the fix: %v", err)
		}
		if want := proxiesImportLog(1, proxiesPath); log.String() != want {
			t.Errorf("log = %q, want %q", log.String(), want)
		}
	})
}

// A database that already has the proxies table runs the later migrations and never reads the proxy file.
func TestOpen_Version3SkipsProxyFile(t *testing.T) {
	dbPath, proxiesPath := migratedDB(t, proxiesVersion)
	writeFile(t, proxiesPath, "a:1:u:p\n")
	var log bytes.Buffer
	s, err := openGuarded(t.Context(), t, dbPath, filepath.Join(t.TempDir(), "accounts.jsonl"), logtest.New(&log))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if log.Len() != 0 || len(mustProxies(t, s)) != 0 {
		t.Errorf("log = %q, proxies = %+v; want no log and no proxy", log.String(), mustProxies(t, s))
	}
	if got, want := dbVersion(t, s.db), latestVersion(t); got != want {
		t.Errorf("user_version = %d, want %d", got, want)
	}
	var tables int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'presets'`).Scan(&tables); err != nil || tables != 1 {
		t.Errorf("presets tables = %d, %v; want 1", tables, err)
	}
}
