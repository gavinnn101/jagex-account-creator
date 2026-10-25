package store

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/waittest"
)

// exportString returns the JSONL export of s.
func exportString(t *testing.T, s *Store) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.ExportJSONL(t.Context(), &buf, AccountFilter{}); err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	return buf.String()
}

func TestWriteAccount_FailedWriteKeepsStore(t *testing.T) {
	s := mustOpen(t)
	a := &account.Account{Email: "a@b.com", Username: "u", Password: "p"}
	if err := s.WriteAccount(t.Context(), a); err != nil {
		t.Fatalf("first WriteAccount: %v", err)
	}
	err := s.WriteAccount(t.Context(), a)
	if err == nil || !strings.HasPrefix(err.Error(), "saving account: ") || !strings.Contains(err.Error(), "UNIQUE constraint failed: accounts.email") {
		t.Fatalf("repeated WriteAccount error = %v, want the UNIQUE failure with the saving account prefix", err)
	}
	if err := s.WriteAccount(t.Context(), &account.Account{Email: "c@d.com", Username: "v", Password: "q"}); err != nil {
		t.Fatalf("WriteAccount after the failure: %v", err)
	}
	want := `{"email":"a@b.com","username":"u","password":"p","created_at":"0001-01-01T00:00:00Z"}` + "\n" +
		`{"email":"c@d.com","username":"v","password":"q","created_at":"0001-01-01T00:00:00Z"}` + "\n"
	if got := exportString(t, s); got != want {
		t.Errorf("export = %q, want %q", got, want)
	}
}

func TestWriteAccount_AfterClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(t.Context(), filepath.Join(dir, "creator.db"), filepath.Join(dir, "accounts.jsonl"), filepath.Join(dir, "proxies.txt"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err = s.WriteAccount(t.Context(), &account.Account{Email: "a@b.com"})
	if err == nil || err.Error() != "saving account: sql: database is closed" {
		t.Errorf("WriteAccount error = %v, want saving account: sql: database is closed", err)
	}
}

// storedRow is the text of the columns that the JSONL record maps to an omitted field.
type storedRow struct {
	totpSecret  string
	backupCodes sql.NullString
	createdAt   string
	proxy       string
	bytes       [4]int64
	unconfirmed int64
	extra       sql.NullString
}

func TestWriteAccount_Columns(t *testing.T) {
	plus2 := time.FixedZone("+02:00", 2*60*60)
	tests := []struct {
		name string
		acct account.Account
		want storedRow
	}{
		{
			name: "minimal",
			acct: account.Account{CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)},
			want: storedRow{createdAt: "2025-01-15T10:30:00.000000000Z"},
		},
		{
			name: "empty backup codes",
			acct: account.Account{TOTPSecret: "S", BackupCodes: []string{}, CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 1, plus2)},
			want: storedRow{totpSecret: "S", createdAt: "2025-01-15T08:30:00.000000001Z"},
		},
		{
			name: "full",
			acct: account.Account{
				TOTPSecret: "S", BackupCodes: []string{"1", "2"}, Proxy: "1.2.3.4:8080",
				BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4, Unconfirmed: true,
			},
			want: storedRow{
				totpSecret: "S", backupCodes: sql.NullString{String: `["1","2"]`, Valid: true},
				createdAt: "0001-01-01T00:00:00.000000000Z", proxy: "1.2.3.4:8080", bytes: [4]int64{1, 2, 3, 4}, unconfirmed: 1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			acct := tt.acct
			if err := s.WriteAccount(t.Context(), &acct); err != nil {
				t.Fatalf("WriteAccount: %v", err)
			}
			var got storedRow
			err := s.db.QueryRowContext(t.Context(), `SELECT totp_secret, backup_codes, created_at, proxy, browser_bytes_sent,
				browser_bytes_received, http_bytes_sent, http_bytes_received, unconfirmed, extra FROM accounts`).Scan(
				&got.totpSecret, &got.backupCodes, &got.createdAt, &got.proxy,
				&got.bytes[0], &got.bytes[1], &got.bytes[2], &got.bytes[3], &got.unconfirmed, &got.extra)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("row = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEncodeJSON_Error(t *testing.T) {
	if _, err := encodeJSON(func() {}); err == nil || !strings.HasPrefix(err.Error(), "encoding JSON: ") {
		t.Errorf("encodeJSON error = %v, want the encoding JSON prefix", err)
	}
}

func TestWriteAccount_Concurrent(t *testing.T) {
	s := mustOpen(t)
	// Two writers are enough to overlap on the one connection. Each write is one disk sync,
	// so a large count reaches the guard on a slow runner disk.
	const n = 4
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if err := s.WriteAccount(t.Context(), &account.Account{Email: fmt.Sprintf("user%d@example.com", i)}); err != nil {
				t.Errorf("WriteAccount %d: %v", i, err)
			}
		})
	}
	// A write goes to disk, so the wait is on the real clock under a guard.
	waittest.Wait(t, &wg)
	var count int
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM accounts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Errorf("count = %d, want %d", count, n)
	}
}
