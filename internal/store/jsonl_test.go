package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/logtest"
)

// importPaths returns a database path and a JSONL path in a new temp dir. A
// non-empty jsonl is written to the JSONL path.
func importPaths(t *testing.T, jsonl string) (dbPath, importPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath, importPath = filepath.Join(dir, "creator.db"), filepath.Join(dir, "accounts.jsonl")
	if jsonl != "" {
		writeFile(t, importPath, jsonl)
	}
	return dbPath, importPath
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// importLog returns the log line of an import of count accounts from path.
func importLog(count int, path string) string {
	var buf bytes.Buffer
	logtest.New(&buf).Info("imported accounts", "count", count, "path", path)
	return buf.String()
}

func TestExportJSONL(t *testing.T) {
	s := mustOpen(t)
	accounts := []account.Account{
		{
			Email: "a@example.com", Username: "userA", Password: "g&N<j>T", TOTPSecret: "JBSWY3DPEHPK3PXP",
			BackupCodes: []string{"12345", "67890"}, CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 123450000, time.UTC),
			Proxy: "1.2.3.4:8080", BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4,
		},
		{Email: "b@example.com", Username: "userB", Password: "p", CreatedAt: time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC), Unconfirmed: true},
	}
	for i := range accounts {
		if err := s.WriteAccount(t.Context(), &accounts[i]); err != nil {
			t.Fatalf("WriteAccount: %v", err)
		}
	}
	want := `{"email":"a@example.com","username":"userA","password":"g&N<j>T","totp_secret":"JBSWY3DPEHPK3PXP",` +
		`"backup_codes":["12345","67890"],"created_at":"2025-01-15T10:30:00.12345Z","proxy":"1.2.3.4:8080",` +
		`"browser_bytes_sent":1,"browser_bytes_received":2,"http_bytes_sent":3,"http_bytes_received":4}` + "\n" +
		`{"email":"b@example.com","username":"userB","password":"p","created_at":"2025-01-16T00:00:00Z","unconfirmed":true}` + "\n"
	if got := exportString(t, s); got != want {
		t.Errorf("export =\n%s\nwant\n%s", got, want)
	}
}

func TestExportJSONL_Filter(t *testing.T) {
	s := mustOpen(t)
	writeFixture(t, s)
	tests := []struct {
		name   string
		filter AccountFilter
		want   string
	}{
		{
			name: "run", filter: AccountFilter{RunID: 2},
			want: `{"email":"b@example.com","username":"userb","password":"p\"b","created_at":"2025-01-16T00:00:00Z",` +
				`"proxy":"2.2.2.2:80","unconfirmed":true}` + "\n",
		},
		// The unknown key of an imported line stays in the JSONL export.
		{
			name: "query", filter: AccountFilter{Query: "C_D"},
			want: `{"email":"c%_d@example.com","username":"c_d","password":"pc","created_at":"2025-01-17T00:00:00Z","note":1}` + "\n",
		},
		{name: "no match", filter: AccountFilter{Query: "zzz"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exportText(t, s, (*Store).ExportJSONL, tt.filter); got != tt.want {
				t.Errorf("export =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestExportJSONL_Empty(t *testing.T) {
	if got := exportString(t, mustOpen(t)); got != "" {
		t.Errorf("export = %q, want none", got)
	}
}

// failWriter fails each write with err.
type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExportJSONL_WriteError(t *testing.T) {
	s := mustOpen(t)
	if err := s.WriteAccount(t.Context(), &account.Account{Email: "a@b.com"}); err != nil {
		t.Fatal(err)
	}
	diskFull := errors.New("disk full")
	err := s.ExportJSONL(t.Context(), failWriter{diskFull}, AccountFilter{})
	if !errors.Is(err, diskFull) || !strings.HasPrefix(err.Error(), "writing account line: ") {
		t.Errorf("ExportJSONL error = %v, want the write failure with the writing account line prefix", err)
	}
}

// rowSQL inserts one account row with the given backup_codes and created_at values.
func rowSQL(codes, createdAt string) string {
	return fmt.Sprintf(`INSERT INTO accounts (email, username, password, backup_codes, created_at)
		VALUES ('a@b.com', 'u', 'p', %s, '%s')`, codes, createdAt)
}

func TestExportJSONL_BadRows(t *testing.T) {
	const columns = `email, username, password, totp_secret, backup_codes, created_at, proxy,
		browser_bytes_sent, browser_bytes_received, http_bytes_sent, http_bytes_received, unconfirmed, extra`
	tests := []struct {
		name  string
		setup string
		want  string
	}{
		{name: "no table", setup: "DROP TABLE accounts", want: "reading accounts: "},
		{
			name:  "NULL in a text column",
			setup: "DROP TABLE accounts; CREATE TABLE accounts (id INTEGER PRIMARY KEY, " + columns + "); INSERT INTO accounts (id) VALUES (1)",
			want:  "reading accounts row: ",
		},
		{name: "bad created_at", setup: rowSQL("NULL", "yesterday"), want: "reading created_at of a@b.com: "},
		{name: "bad backup codes", setup: rowSQL("'[1]'", timeLayout), want: "reading backup_codes of a@b.com: "},
		{
			// The abs of the smallest integer overflows on the second row, after the first row reached the caller.
			name: "error on a later row",
			setup: `DROP TABLE accounts; CREATE TABLE t (id INTEGER PRIMARY KEY); INSERT INTO t VALUES (1), (2);
				CREATE VIEW accounts AS SELECT id, 'a' || id || '@b.com' AS email, 'u' AS username,
				'p' AS password, '' AS totp_secret, NULL AS backup_codes, '` + timeLayout + `' AS created_at, '' AS proxy,
				abs(-9223372036854775806 - id) AS browser_bytes_sent, 0 AS browser_bytes_received, 0 AS http_bytes_sent,
				0 AS http_bytes_received, 0 AS unconfirmed, NULL AS extra FROM t`,
			want: "reading accounts: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			if _, err := s.db.ExecContext(t.Context(), tt.setup); err != nil {
				t.Fatalf("setup: %v", err)
			}
			err := s.ExportJSONL(t.Context(), io.Discard, AccountFilter{})
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("ExportJSONL error = %v, want the prefix %q", err, tt.want)
			}
		})
	}
}

func TestOpen_ImportRoundTrip(t *testing.T) {
	jsonl := `{"email":"a@example.com","username":"userA","password":"g&N<j>T","totp_secret":"JBSWY3DPEHPK3PXP",` +
		`"backup_codes":["12345","67890"],"created_at":"2025-01-15T10:30:00.12345Z","proxy":"1.2.3.4:8080",` +
		`"browser_bytes_sent":1,"browser_bytes_received":2,"http_bytes_sent":3,"http_bytes_received":4}` + "\n" +
		`{"email":"b@example.com","username":"userB","password":"p","created_at":"2025-01-16T00:00:00Z","unconfirmed":true}` + "\n" +
		`{"email":"c@example.com","username":"userC","password":"p","created_at":"2024-12-01T08:00:00.123456789Z",` +
		`"proxy":"5.6.7.8:3128","bytes_sent":100,"bytes_received":200}` + "\n" +
		`{"email":"d@example.com","username":"userD","password":"p","totp_secret":"S","created_at":"2025-02-01T00:00:00Z",` +
		`"unconfirmed":true}` + "\n"
	dbPath, importPath := importPaths(t, jsonl)
	var log bytes.Buffer
	s, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(&log))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := exportString(t, s); got != jsonl {
		t.Errorf("export =\n%s\nwant\n%s", got, jsonl)
	}
	if want := importLog(4, importPath); log.String() != want {
		t.Errorf("log = %q, want %q", log.String(), want)
	}
	data, err := os.ReadFile(importPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != jsonl {
		t.Error("the import changed its file")
	}
}

func TestOpen_ImportKeepsUnknownKeys(t *testing.T) {
	dbPath, importPath := importPaths(t,
		`{"email":"a@b.com", "bytes_sent" : 5,"username":"u","password":"p","note":{"a": [1, "<&>"]},"created_at":"2025-01-15T10:30:00Z","bytes_received":6}`+"\n")
	s, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var extra string
	if err := s.db.QueryRowContext(t.Context(), "SELECT extra FROM accounts").Scan(&extra); err != nil {
		t.Fatal(err)
	}
	// The keys and values keep the spelling of the line.
	if want := `{"bytes_sent":5,"note":{"a": [1, "<&>"]},"bytes_received":6}`; extra != want {
		t.Errorf("extra = %s, want %s", extra, want)
	}
	want := `{"email":"a@b.com","username":"u","password":"p","created_at":"2025-01-15T10:30:00Z",` +
		`"bytes_sent":5,"note":{"a": [1, "<&>"]},"bytes_received":6}` + "\n"
	if got := exportString(t, s); got != want {
		t.Errorf("export = %s, want %s", got, want)
	}
}

func TestOpen_ImportFileStates(t *testing.T) {
	t.Run("no file imports nothing and logs nothing", func(t *testing.T) {
		dbPath, importPath := importPaths(t, "")
		var log bytes.Buffer
		s, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(&log))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if log.Len() != 0 {
			t.Errorf("log = %q, want none", log.String())
		}
		if got := exportString(t, s); got != "" {
			t.Errorf("export = %q, want none", got)
		}
	})

	t.Run("an empty file logs a count of 0", func(t *testing.T) {
		dbPath, importPath := importPaths(t, "")
		writeFile(t, importPath, "")
		var log bytes.Buffer
		if _, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(&log)); err != nil {
			t.Fatalf("Open: %v", err)
		}
		if want := importLog(0, importPath); log.String() != want {
			t.Errorf("log = %q, want %q", log.String(), want)
		}
	})

	t.Run("CRLF line ends", func(t *testing.T) {
		dbPath, importPath := importPaths(t, goodLine+"\r\n")
		s, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(io.Discard))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got := exportString(t, s); got != goodLine+"\n" {
			t.Errorf("export = %q, want %q", got, goodLine+"\n")
		}
	})

	t.Run("a dir fails the read", func(t *testing.T) {
		dbPath, _ := importPaths(t, "")
		importPath := t.TempDir()
		_, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(io.Discard))
		if want := fmt.Sprintf("setting up database %s: importing accounts: ", dbPath); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Open error = %v, want the prefix %q", err, want)
		}
	})
}

func TestOpen_SecondOpenDoesNotImport(t *testing.T) {
	first := `{"email":"a@b.com","username":"u","password":"p","created_at":"2025-01-15T10:30:00Z"}` + "\n"
	dbPath, importPath := importPaths(t, first)
	s, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	writeFile(t, importPath, `{"email":"c@d.com","username":"v","password":"q","created_at":"2025-01-15T10:30:00Z"}`+"\n")
	var log bytes.Buffer
	s, err = openGuarded(t.Context(), t, dbPath, importPath, logtest.New(&log))
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if log.Len() != 0 {
		t.Errorf("log = %q, want none", log.String())
	}
	if got := exportString(t, s); got != first {
		t.Errorf("export = %q, want %q", got, first)
	}
}

const goodLine = `{"email":"a@b.com","username":"u","password":"secret","created_at":"2025-01-15T10:30:00Z"}`

// assertImportFails checks that Open fails with want and that the database
// stays new: a second Open with a good file imports that file.
func assertImportFails(t *testing.T, line, want string) {
	t.Helper()
	dbPath, importPath := importPaths(t, goodLine+"\n"+line+"\n")
	_, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(io.Discard))
	want = fmt.Sprintf("setting up database %s: importing %s line 2: %s", dbPath, importPath, want)
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("Open error = %v, want the prefix %q", err, want)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error %q holds the line text", err)
	}
	writeFile(t, importPath, goodLine+"\n")
	var log bytes.Buffer
	if _, err := openGuarded(t.Context(), t, dbPath, importPath, logtest.New(&log)); err != nil {
		t.Fatalf("Open after the fix: %v", err)
	}
	if got := importLog(1, importPath); log.String() != got {
		t.Errorf("log = %q, want %q", log.String(), got)
	}
}

func TestOpen_ImportBadLine(t *testing.T) {
	tests := []struct{ name, line string }{
		{name: "blank", line: ""},
		{name: "null", line: `null`},
		{name: "array", line: `["secret"]`},
		{name: "number key", line: `{1:"secret"}`},
		{name: "no value", line: `{"email":}`},
		{name: "no colon", line: `{"email" "secret"}`},
		{name: "wrong type", line: `{"email":1,"password":"secret"}`},
		{name: "trailing data", line: `{"email":"x@y.com","password":"secret"} x`},
		{name: "cut short", line: `{"email":"x@y.com","password":"secret"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertImportFails(t, tt.line, "not a JSON account object")
		})
	}
}

func TestOpen_ImportRefusedRow(t *testing.T) {
	tests := []struct{ name, line, want string }{
		{name: "repeated email", line: goodLine, want: "saving account: "},
		{name: "explicit empty proxy", line: `{"email":"x@y.com","username":"u","password":"secret","created_at":"2025-01-15T10:30:00Z","proxy":""}`},
		{name: "not UTC", line: `{"email":"x@y.com","username":"u","password":"secret","created_at":"2025-01-15T12:30:00+02:00"}`},
		{name: "no email", line: `{"username":"u","password":"secret","created_at":"2025-01-15T10:30:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = "the stored row differs from the line"
			}
			assertImportFails(t, tt.line, want)
		})
	}
}
