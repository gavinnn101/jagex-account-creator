// Package store keeps the created accounts and the run history in one SQLite database.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// dsnParams turn on WAL and foreign keys, wait up to 5 s for a lock, and take
// the write lock when a transaction starts.
const dsnParams = "?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=1&_txlock=immediate"

// Store holds the accounts database.
type Store struct {
	db *sql.DB
}

// dbtx is the method set that *sql.DB and *sql.Tx share, so the import runs
// the same statements on its transaction.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Open opens the database at path and runs its pending migrations. On a new
// database, it also imports the accounts of the JSONL file at importPath when
// that file exists. When the set-up creates the proxies table, it imports the
// proxy file at proxiesPath when that file exists. The caller must call Close.
func Open(ctx context.Context, path, importPath, proxiesPath string, logger *slog.Logger) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := setUp(ctx, db, migrations, importPath, proxiesPath, logger); err != nil {
		return nil, errors.Join(fmt.Errorf("setting up database %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// openDB creates the file at path when it is missing and returns a pool of one
// connection to it.
func openDB(path string) (*sql.DB, error) {
	// The connector checks the DSN before the file exists.
	connector, err := sqlite.NewConnector(path + dsnParams)
	if err != nil {
		return nil, fmt.Errorf("opening database %s: %w", path, err)
	}
	// SQLite gives the -wal and -shm files the mode of the database file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		// Nothing was written, so the close can lose nothing.
		_ = f.Close()
	} else if !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("creating database file: %w", err)
	}
	db := sql.OpenDB(connector)
	// The one connection serializes the writes. A write waits for it and never fails on a lock.
	db.SetMaxOpenConns(1)
	return db, nil
}

// Close closes the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("closing database: %w", err)
	}
	return nil
}

// proxiesVersion is the schema version of the migration that creates the proxies table.
const proxiesVersion = 3

// setUp runs the pending migrations of files in one transaction. On a new
// database, the accounts import runs after the last migration in the same
// transaction, because its INSERT matches the latest schema. The proxies import
// runs in the same way when the set-up applies the proxies migration, so an
// existing database imports the proxy file once too. A failure changes nothing.
func setUp(ctx context.Context, db *sql.DB, files fs.FS, importPath, proxiesPath string, logger *slog.Logger) error {
	imported, found := 0, false
	var proxies ProxyImport
	err := withTx(ctx, db, "set-up", func(tx *sql.Tx) error {
		version, err := userVersion(ctx, tx)
		if err != nil {
			return err
		}
		if err := migrate(ctx, tx, files, version); err != nil {
			return err
		}
		if version == 0 {
			if imported, found, err = importJSONL(ctx, tx, importPath); err != nil {
				return err
			}
		}
		if version < proxiesVersion {
			proxies, err = importProxyFile(ctx, tx, proxiesPath)
		}
		return err
	})
	if err != nil {
		return err
	}
	if found {
		logger.InfoContext(ctx, "imported accounts", "count", imported, "path", importPath)
	}
	if proxies.Added > 0 {
		logger.InfoContext(ctx, "imported proxies", "count", proxies.Added, "path", proxiesPath)
	}
	return nil
}

// withTx runs fn in one transaction of db and commits it when fn returns no
// error. what names the transaction in an error.
func withTx(ctx context.Context, db *sql.DB, what string, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting the %s transaction: %w", what, err)
	}
	// After a commit, the rollback does nothing.
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing the %s transaction: %w", what, err)
	}
	return nil
}

// migrate runs each file in the migrations dir of files whose number prefix is
// above version, in name order. Each file sets its own number as the new
// user_version, and migrate checks that it did.
func migrate(ctx context.Context, tx *sql.Tx, files fs.FS, version int) error {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		prefix, _, _ := strings.Cut(name, "_")
		n, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("numbering migration %s: %w", name, err)
		}
		if n <= version {
			continue
		}
		script, err := fs.ReadFile(files, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("reading migration: %w", err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("running migration %s: %w", name, err)
		}
		got, err := userVersion(ctx, tx)
		if err != nil {
			return err
		}
		if got != n {
			return fmt.Errorf("migration %s set the schema version to %d, want %d", name, got, n)
		}
	}
	return nil
}

// userVersion returns the schema version that the last migration set.
func userVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("reading the schema version: %w", err)
	}
	return version, nil
}
