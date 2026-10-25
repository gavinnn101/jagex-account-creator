package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"jagex_account_creator/internal/account"
)

// timeLayout is fixed-width UTC, so the stored text sorts in time order.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

const insertAccountSQL = `INSERT INTO accounts (email, username, password, totp_secret, backup_codes, created_at, proxy,
	browser_bytes_sent, browser_bytes_received, http_bytes_sent, http_bytes_received, unconfirmed, extra)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// fromAccountsSQL joins each account to the attempt that saved it. attempts.account_id is unique, so the join
// gives one row for each account.
const fromAccountsSQL = `
FROM accounts a LEFT JOIN attempts t ON t.account_id = a.id`

// selectAccountColumnsSQL lists the columns in the order of accountRow.dest.
const selectAccountColumnsSQL = `SELECT a.id, coalesce(t.run_id, 0), a.email, a.username, a.password, a.totp_secret,
	a.backup_codes, a.created_at, a.proxy, a.browser_bytes_sent, a.browser_bytes_received, a.http_bytes_sent,
	a.http_bytes_received, a.unconfirmed, a.extra` + fromAccountsSQL

// accountFilterSQL takes the values of AccountFilter.args. Each filter tests for its empty value first, so one
// static statement covers every mix. The search uses instr, because a % or _ in a LIKE pattern matches any text.
// Limitation: SQLite's lower folds ASCII letters only, so a non-ASCII letter matches only in its own case.
const accountFilterSQL = `
WHERE (?1 = 0 OR t.run_id = ?1)
	AND (?2 IS NULL OR a.unconfirmed = ?2)
	AND (?3 IS NULL OR (a.totp_secret != '') = ?3)
	AND (?4 = '' OR a.proxy = ?4)
	AND (?5 = '' OR a.created_at >= ?5)
	AND (?6 = '' OR a.created_at < ?6)
	AND (?7 = '' OR instr(lower(a.email), lower(?7)) > 0 OR instr(lower(a.username), lower(?7)) > 0)`

const selectAccountSQL = selectAccountColumnsSQL + ` WHERE a.id = ?`

const confirmAccountSQL = `UPDATE accounts SET unconfirmed = 0 WHERE id = ?`

// A before of 0 is the first page.
const selectAccountPageSQL = selectAccountColumnsSQL + accountFilterSQL + ` AND (?8 = 0 OR a.id < ?8) ORDER BY a.id DESC LIMIT ?9`

const selectAccountsSQL = selectAccountColumnsSQL + accountFilterSQL + ` ORDER BY a.id`

// countAccountsSQL takes only the filter args, so the total does not change with the page.
const countAccountsSQL = `SELECT count(*)` + fromAccountsSQL + accountFilterSQL

// AccountFilter selects the accounts of a read or an export. The zero value selects every account.
type AccountFilter struct {
	RunID       int64     // the run of the attempt that saved the account. 0 means any run
	Unconfirmed *bool     // nil means any
	HasTOTP     *bool     // whether the account has a TOTP secret. nil means any
	Proxy       string    // an exact match. Empty means any
	CreatedFrom time.Time // inclusive. Zero means no bound
	CreatedTo   time.Time // exclusive. Zero means no bound
	Query       string    // a substring of the email or the username, in any case. Empty means any
}

// args returns the values of accountFilterSQL in the order of its parameters.
func (f *AccountFilter) args() []any {
	return []any{f.RunID, nullBool(f.Unconfirmed), nullBool(f.HasTOTP), f.Proxy,
		timeBound(f.CreatedFrom), timeBound(f.CreatedTo), f.Query}
}

// StoredAccount is one account of the store. The keys of an imported line that
// account.Account does not know stay in the JSONL export only.
type StoredAccount struct {
	ID      int64
	RunID   int64 // the run of the attempt that saved the account, or 0 when no attempt links it
	Account account.Account
}

// WriteAccount saves acct as a new row. Each call returns the error of its own
// row, and a failed call does not stop later calls.
func (s *Store) WriteAccount(ctx context.Context, acct *account.Account) error {
	return insertAccount(ctx, s.db, acct, nil)
}

// insertAccount saves acct with extra, the unknown keys of an imported line as
// a JSON object. extra is nil for an account that the run created.
func insertAccount(ctx context.Context, q dbtx, acct *account.Account, extra []byte) error {
	// With no codes, the column stays NULL, as the record omits the key.
	var codes []byte
	var err error
	if len(acct.BackupCodes) > 0 {
		codes, err = encodeJSON(acct.BackupCodes)
	}
	if err == nil {
		_, err = q.ExecContext(ctx, insertAccountSQL,
			acct.Email, acct.Username, acct.Password, acct.TOTPSecret, nullText(codes),
			acct.CreatedAt.UTC().Format(timeLayout), acct.Proxy,
			acct.BrowserBytesSent, acct.BrowserBytesReceived, acct.HTTPBytesSent, acct.HTTPBytesReceived,
			acct.Unconfirmed, nullText(extra))
	}
	if err != nil {
		return fmt.Errorf("saving account: %w", err)
	}
	return nil
}

// ConfirmAccount marks the account whose id is id as confirmed. A second call
// changes nothing. The error matches ErrNotFound when no account has that id.
func (s *Store) ConfirmAccount(ctx context.Context, id int64) error {
	if err := execByID(ctx, s.db, confirmAccountSQL, id); err != nil {
		return fmt.Errorf("confirming account %d: %w", id, err)
	}
	return nil
}

// Account returns the account whose id is id. The error matches ErrNotFound when no account has that id.
func (s *Store) Account(ctx context.Context, id int64) (StoredAccount, error) {
	rows, err := queryRows(ctx, s.db, "account", (*accountRow).dest, selectAccountSQL, id)
	if err != nil {
		return StoredAccount{}, err
	}
	accounts, err := convertRows(rows, (*accountRow).stored)
	if err != nil {
		return StoredAccount{}, err
	}
	if len(accounts) == 0 {
		return StoredAccount{}, fmt.Errorf("reading account %d: %w", id, ErrNotFound)
	}
	return accounts[0], nil
}

// AccountPage is one page of the accounts that a filter selects.
type AccountPage struct {
	Accounts []StoredAccount
	Next     int64 // the before of the next page, or 0 on the last page
	Total    int64 // the count of the accounts that the filter selects, the same on each page
}

// Accounts returns at most limit accounts that f selects with an id below
// before, newest first, as Runs pages the runs. One read transaction holds
// the page and its total, so both count the same rows.
func (s *Store) Accounts(ctx context.Context, f AccountFilter, before int64, limit int) (AccountPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AccountPage{}, fmt.Errorf("reading accounts: %w", err)
	}
	// The transaction only reads, so the rollback loses nothing.
	defer func() { _ = tx.Rollback() }()
	var page AccountPage
	if err := tx.QueryRowContext(ctx, countAccountsSQL, f.args()...).Scan(&page.Total); err != nil {
		return AccountPage{}, fmt.Errorf("counting accounts: %w", err)
	}
	rows, err := queryRows(ctx, tx, "accounts", (*accountRow).dest, selectAccountPageSQL, append(f.args(), before, limit+1)...)
	if err != nil {
		return AccountPage{}, err
	}
	rows, page.Next = cutPage(rows, limit, func(r *accountRow) int64 { return r.a.ID })
	if page.Accounts, err = convertRows(rows, (*accountRow).stored); err != nil {
		return AccountPage{}, err
	}
	return page, nil
}

// exportRows returns every account that f selects, in id order.
func (s *Store) exportRows(ctx context.Context, f AccountFilter) ([]StoredAccount, error) {
	rows, err := queryRows(ctx, s.db, "accounts", (*accountRow).dest, selectAccountsSQL, f.args()...)
	if err != nil {
		return nil, err
	}
	return convertRows(rows, (*accountRow).stored)
}

// accountRow is one row of selectAccountColumnsSQL, with the backup codes, the
// created_at and the unknown imported keys as stored.
type accountRow struct {
	a            StoredAccount
	codes, extra sql.NullString
	createdAt    string
}

func (x *accountRow) dest() []any {
	a := &x.a.Account
	return []any{&x.a.ID, &x.a.RunID, &a.Email, &a.Username, &a.Password, &a.TOTPSecret, &x.codes, &x.createdAt, &a.Proxy,
		&a.BrowserBytesSent, &a.BrowserBytesReceived, &a.HTTPBytesSent, &a.HTTPBytesReceived, &a.Unconfirmed, &x.extra}
}

// stored returns the account of x with its created_at and its backup codes parsed.
func (x *accountRow) stored() (StoredAccount, error) {
	sa := x.a
	var err error
	if sa.Account.CreatedAt, err = time.Parse(timeLayout, x.createdAt); err != nil {
		return sa, fmt.Errorf("reading created_at of %s: %w", sa.Account.Email, err)
	}
	if x.codes.Valid {
		if err := json.Unmarshal([]byte(x.codes.String), &sa.Account.BackupCodes); err != nil {
			return sa, fmt.Errorf("reading backup_codes of %s: %w", sa.Account.Email, err)
		}
	}
	return sa, nil
}

// nullBool stores *b, or NULL when b is nil.
func nullBool(b *bool) sql.NullBool {
	if b == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *b, Valid: true}
}

// timeBound returns t in the stored form, so the text compare orders it with created_at. A zero t is no bound.
func timeBound(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

// nullText stores b as TEXT, or as NULL when b is nil.
func nullText(b []byte) sql.NullString {
	return sql.NullString{String: string(b), Valid: b != nil}
}

// encodeJSON encodes v with HTML escaping off and no newline at the end, as
// the JSONL file had it.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding JSON: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
