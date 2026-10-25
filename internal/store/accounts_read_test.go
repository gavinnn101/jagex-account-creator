package store

import (
	"context"
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

// fixtureAccounts are the accounts that writeFixture saves, in id order.
func fixtureAccounts() []account.Account {
	return []account.Account{
		{
			Email: "a@example.com", Username: "usera", Password: "p,a", TOTPSecret: "S1", BackupCodes: []string{"11", "22"},
			CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 500000000, time.UTC), Proxy: "1.1.1.1:80",
			BrowserBytesSent: 1, BrowserBytesReceived: 2, HTTPBytesSent: 3, HTTPBytesReceived: 4,
		},
		{
			Email: "b@example.com", Username: "userb", Password: `p"b`, CreatedAt: time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC),
			Proxy: "2.2.2.2:80", Unconfirmed: true,
		},
		{Email: "c%_d@example.com", Username: "c_d", Password: "pc", CreatedAt: time.Date(2025, 1, 17, 0, 0, 0, 0, time.UTC)},
		{
			Email: "e@example.com", Username: "MixedCase", Password: "@pe", TOTPSecret: "S4",
			CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC), Proxy: "1.1.1.1:80", Unconfirmed: true,
		},
	}
}

// fixtureExtra is the unknown key of account 3, an imported account.
const fixtureExtra = `{"note":1}`

// writeFixture saves the fixture accounts. Run 1 links accounts 1 and 4, and run 2 links account 2. No attempt links
// account 3. Run 3 holds one attempt that saved no account.
func writeFixture(t *testing.T, s *Store) {
	t.Helper()
	for range 3 {
		mustWriteRun(t, s)
	}
	accts := fixtureAccounts()
	for i := range accts {
		var extra []byte
		if i == 2 {
			extra = []byte(fixtureExtra)
		}
		if err := insertAccount(t.Context(), s.db, &accts[i], extra); err != nil {
			t.Fatalf("insertAccount: %v", err)
		}
	}
	mustWriteAttempt(t, s, &Attempt{RunID: 1}, "a@example.com")
	mustWriteAttempt(t, s, &Attempt{RunID: 2}, "b@example.com")
	mustWriteAttempt(t, s, &Attempt{RunID: 1}, "e@example.com")
	mustWriteAttempt(t, s, &Attempt{RunID: 3, Category: "unknown", Error: "boom"}, "")
}

// filterTest is one filter over the fixture and the ids that it selects, newest first.
type filterTest struct {
	name   string
	filter AccountFilter
	want   []int64
}

func filterTests() []filterTest {
	// at returns a time near the creation of accounts 1 and 4.
	at := func(hour, nsec int, loc *time.Location) time.Time {
		return time.Date(2025, 1, 15, hour, 30, 0, nsec, loc)
	}
	return []filterTest{
		{name: "empty", want: []int64{4, 3, 2, 1}},
		{name: "run 1", filter: AccountFilter{RunID: 1}, want: []int64{4, 1}},
		{name: "run 2", filter: AccountFilter{RunID: 2}, want: []int64{2}},
		{name: "run with no saved account", filter: AccountFilter{RunID: 3}, want: []int64{}},
		{name: "unconfirmed", filter: AccountFilter{Unconfirmed: new(true)}, want: []int64{4, 2}},
		{name: "confirmed", filter: AccountFilter{Unconfirmed: new(false)}, want: []int64{3, 1}},
		{name: "has TOTP", filter: AccountFilter{HasTOTP: new(true)}, want: []int64{4, 1}},
		{name: "no TOTP", filter: AccountFilter{HasTOTP: new(false)}, want: []int64{3, 2}},
		{name: "proxy", filter: AccountFilter{Proxy: "2.2.2.2:80"}, want: []int64{2}},
		{name: "proxy is an exact match", filter: AccountFilter{Proxy: "2.2.2.2"}, want: []int64{}},
		// Account 4 is at 10:30:00 UTC and account 1 at 10:30:00.5 UTC.
		{name: "from is inclusive", filter: AccountFilter{CreatedFrom: at(10, 0, time.UTC)}, want: []int64{4, 3, 2, 1}},
		{name: "from in another zone", filter: AccountFilter{CreatedFrom: at(12, 500000000, plus2())}, want: []int64{3, 2, 1}},
		{name: "to is exclusive", filter: AccountFilter{CreatedTo: at(12, 500000000, plus2())}, want: []int64{4}},
		// The raw RFC 3339 text 2025-01-15T10:30:00Z sorts after both stored times.
		{name: "to with no fraction digits", filter: AccountFilter{CreatedTo: at(10, 0, time.UTC)}, want: []int64{}},
		{
			name:   "from and to",
			filter: AccountFilter{CreatedFrom: at(10, 1, time.UTC), CreatedTo: time.Date(2025, 1, 17, 0, 0, 0, 0, time.UTC)},
			want:   []int64{2, 1},
		},
		{name: "query percent", filter: AccountFilter{Query: "%"}, want: []int64{3}},
		{name: "query underscore", filter: AccountFilter{Query: "_"}, want: []int64{3}},
		{name: "query mixed case of the email", filter: AccountFilter{Query: "B@Example"}, want: []int64{2}},
		{name: "query of a mixed-case username", filter: AccountFilter{Query: "mixedcase"}, want: []int64{4}},
		{name: "query with no match", filter: AccountFilter{Query: "zzz"}, want: []int64{}},
		{name: "run and TOTP and unconfirmed", filter: AccountFilter{RunID: 1, HasTOTP: new(true), Unconfirmed: new(true)}, want: []int64{4}},
		{name: "run and query", filter: AccountFilter{RunID: 1, Query: "USERA"}, want: []int64{1}},
		{name: "proxy and from", filter: AccountFilter{Proxy: "1.1.1.1:80", CreatedFrom: at(10, 1, time.UTC)}, want: []int64{1}},
	}
}

// accountIDs returns the id of each account.
func accountIDs(accounts []StoredAccount) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	return ids
}

func TestAccounts_Filters(t *testing.T) {
	s := mustOpen(t)
	writeFixture(t, s)
	for _, tt := range filterTests() {
		t.Run(tt.name, func(t *testing.T) {
			page, err := s.Accounts(t.Context(), tt.filter, 0, 10)
			if err != nil {
				t.Fatalf("Accounts: %v", err)
			}
			if ids := accountIDs(page.Accounts); !reflect.DeepEqual(ids, tt.want) || page.Next != 0 || page.Total != int64(len(tt.want)) {
				t.Errorf("Accounts = %v, next %d, total %d, want %v, next 0, total %d", ids, page.Next, page.Total, tt.want, len(tt.want))
			}
		})
	}
}

func TestAccounts_Paging(t *testing.T) {
	for _, tt := range pagingTests() {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			for i := range tt.rows {
				mustWriteAccount(t, s, fmt.Sprintf("u%d@b.com", i), "u", false)
			}
			got := collectPages(t, func(before int64) ([]int64, int64, error) {
				page, err := s.Accounts(t.Context(), AccountFilter{}, before, 2)
				// Each page counts every row, also the rows at or above before.
				if err == nil && page.Total != int64(tt.rows) {
					t.Errorf("total of the page before %d = %d, want %d", before, page.Total, tt.rows)
				}
				return accountIDs(page.Accounts), page.Next, err
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pages = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccounts_PagingWithAFilter(t *testing.T) {
	s := mustOpen(t)
	writeFixture(t, s)
	filter := AccountFilter{Unconfirmed: new(true)}
	var totals []int64
	got := collectPages(t, func(before int64) ([]int64, int64, error) {
		page, err := s.Accounts(t.Context(), filter, before, 1)
		totals = append(totals, page.Total)
		return accountIDs(page.Accounts), page.Next, err
	})
	if want := [][]int64{{4}, {2}}; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(totals, []int64{2, 2}) {
		t.Errorf("pages = %v, totals %v, want %v, totals [2 2]", got, totals, want)
	}
	// The first page has a next and a total that are not zero and differ, so a swap of the two fails.
	first, err := s.Accounts(t.Context(), filter, 0, 1)
	if err != nil || first.Next != 4 || first.Total != 2 {
		t.Errorf("first page = next %d, total %d, %v, want next 4, total 2", first.Next, first.Total, err)
	}
}

func TestAccounts_QueryFails(t *testing.T) {
	tests := []struct {
		name  string
		match string
		want  string
	}{
		{name: "the total", match: "SELECT count(*)", want: "counting accounts: "},
		{name: "the page", match: "ORDER BY a.id DESC", want: "reading accounts: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := faultDB(t, &faultConnector{match: tt.match, fail: 1})
			if err := setUp(t.Context(), db, migrations, filepath.Join(t.TempDir(), "accounts.jsonl"), missingProxies(t), logtest.New(io.Discard)); err != nil {
				t.Fatal(err)
			}
			s := &Store{db: db}
			_, err := s.Accounts(t.Context(), AccountFilter{}, 0, 10)
			if !errors.Is(err, errFault) || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("Accounts error = %v, want the fault with the prefix %q", err, tt.want)
			}
		})
	}
}

func TestAccount_ReadBack(t *testing.T) {
	s := mustOpen(t)
	writeFixture(t, s)
	accts := fixtureAccounts()
	for _, want := range []StoredAccount{
		{ID: 1, RunID: 1, Account: accts[0]},
		{ID: 2, RunID: 2, Account: accts[1]},
		// The imported account has no run, and its unknown key is not part of the account.
		{ID: 3, Account: accts[2]},
		{ID: 4, RunID: 1, Account: accts[3]},
	} {
		got, err := s.Account(t.Context(), want.ID)
		if err != nil {
			t.Fatalf("Account %d: %v", want.ID, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Account %d = %+v, want %+v", want.ID, got, want)
		}
	}
}

func TestAccount_NotFound(t *testing.T) {
	s := mustOpen(t)
	writeFixture(t, s)
	_, err := s.Account(t.Context(), 5)
	if !errors.Is(err, ErrNotFound) || err.Error() != "reading account 5: not found" {
		t.Errorf("Account error = %v, want reading account 5: not found", err)
	}
}

func TestConfirmAccount(t *testing.T) {
	s := mustOpen(t)
	writeFixture(t, s)
	// Account 2 is unconfirmed, and a second confirm of it changes nothing. Account 1 is already confirmed.
	for _, id := range []int64{2, 2, 1} {
		if err := s.ConfirmAccount(t.Context(), id); err != nil {
			t.Fatalf("ConfirmAccount(%d): %v", id, err)
		}
	}
	got, err := s.Accounts(t.Context(), AccountFilter{Unconfirmed: new(true)}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ids := accountIDs(got.Accounts); !reflect.DeepEqual(ids, []int64{4}) {
		t.Errorf("unconfirmed accounts = %v, want [4]", ids)
	}
	want := fixtureAccounts()[1]
	want.Unconfirmed = false
	if a, err := s.Account(t.Context(), 2); err != nil || !reflect.DeepEqual(a.Account, want) {
		t.Errorf("Account 2 = %+v, %v, want %+v", a.Account, err, want)
	}
	// The run history counts read the column, so run 2 has no unconfirmed account left.
	counts, err := s.CountRuns(t.Context(), []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if c1, c2 := counts[1].Unconfirmed, counts[2].Unconfirmed; c1 != 1 || c2 != 0 {
		t.Errorf("unconfirmed counts of runs 1 and 2 = %d, %d, want 1, 0", c1, c2)
	}
}

func TestConfirmAccount_NotFound(t *testing.T) {
	s := mustOpen(t)
	writeFixture(t, s)
	err := s.ConfirmAccount(t.Context(), 5)
	if !errors.Is(err, ErrNotFound) || err.Error() != "confirming account 5: not found" {
		t.Errorf("ConfirmAccount error = %v, want confirming account 5: not found", err)
	}
}

func TestConfirmAccount_AfterClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(t.Context(), filepath.Join(dir, "creator.db"), filepath.Join(dir, "accounts.jsonl"), filepath.Join(dir, "proxies.txt"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err = s.ConfirmAccount(t.Context(), 1)
	if err == nil || err.Error() != "confirming account 1: sql: database is closed" || errors.Is(err, ErrNotFound) {
		t.Errorf("ConfirmAccount error = %v, want confirming account 1: sql: database is closed", err)
	}
}

// accountRead is one read of the accounts with an empty filter.
type accountRead struct {
	name string
	read func(ctx context.Context, s *Store) error
}

func accountReads() []accountRead {
	return []accountRead{
		{name: "Account", read: func(ctx context.Context, s *Store) error { _, err := s.Account(ctx, 1); return err }},
		{name: "Accounts", read: func(ctx context.Context, s *Store) error {
			_, err := s.Accounts(ctx, AccountFilter{}, 0, 10)
			return err
		}},
		{name: "ExportJSONL", read: func(ctx context.Context, s *Store) error { return s.ExportJSONL(ctx, io.Discard, AccountFilter{}) }},
		{name: "ExportCSV", read: func(ctx context.Context, s *Store) error { return s.ExportCSV(ctx, io.Discard, AccountFilter{}) }},
		{name: "ExportUserPass", read: func(ctx context.Context, s *Store) error { return s.ExportUserPass(ctx, io.Discard, AccountFilter{}) }},
	}
}

func TestAccountReads_BadRow(t *testing.T) {
	for _, r := range accountReads() {
		t.Run(r.name, func(t *testing.T) {
			s := mustOpen(t)
			if _, err := s.db.ExecContext(t.Context(), rowSQL("NULL", "yesterday")); err != nil {
				t.Fatalf("setup: %v", err)
			}
			const want = "reading created_at of a@b.com: "
			if err := r.read(t.Context(), s); err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("%s error = %v, want the prefix %q", r.name, err, want)
			}
		})
	}
}

func TestAccountReads_AfterClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(t.Context(), filepath.Join(dir, "creator.db"), filepath.Join(dir, "accounts.jsonl"), filepath.Join(dir, "proxies.txt"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, r := range accountReads() {
		t.Run(r.name, func(t *testing.T) {
			want := "reading accounts: sql: database is closed"
			if r.name == "Account" {
				want = "reading account: sql: database is closed"
			}
			if err := r.read(t.Context(), s); err == nil || err.Error() != want {
				t.Errorf("%s error = %v, want %s", r.name, err, want)
			}
		})
	}
}
