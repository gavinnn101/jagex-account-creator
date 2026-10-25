package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/logtest"
)

const exportLine = `{"email":"user@example.com","username":"user","password":"pw","created_at":"2025-01-15T10:30:00Z"}` + "\n"

// exportPaths returns a config whose database is in a new temp dir, and the
// path of the export file in that dir.
func exportPaths(t *testing.T) (cfg *config.Config, out string) {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{Database: filepath.Join(dir, "creator.db")}, filepath.Join(dir, "export.jsonl")
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The run opens the store as the full run does, so a new database imports the accounts.jsonl beside it.
func TestRunExport(t *testing.T) {
	cfg, out := exportPaths(t)
	importPath := filepath.Join(filepath.Dir(cfg.Database), "accounts.jsonl")
	writeTestFile(t, importPath, exportLine)
	var log bytes.Buffer

	if err := runExport(t.Context(), logtest.New(&log), cfg, out); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	got := readTestFile(t, out)
	if got != exportLine {
		t.Errorf("export file = %q, want %q", got, exportLine)
	}
	s, err := openStore(t.Context(), logtest.New(io.Discard), cfg)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer func() { _ = s.Close() }()
	if stored := readRecords(t, s); got != stored {
		t.Errorf("export file = %q, want the store export %q", got, stored)
	}
	var want bytes.Buffer
	logtest.New(&want).Info("imported accounts", "count", 1, "path", importPath)
	logtest.New(&want).Info("exported accounts", "path", out)
	if log.String() != want.String() {
		t.Errorf("log =\n%s\nwant\n%s", log.String(), want.String())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("export file mode = %o, want 600", got)
		}
	}
}

func TestRunExport_Errors(t *testing.T) {
	t.Run("an empty -out touches no file", func(t *testing.T) {
		cfg, _ := exportPaths(t)
		err := runExport(t.Context(), logtest.New(io.Discard), cfg, "")
		if err == nil || err.Error() != "export mode needs -out" {
			t.Fatalf("runExport error = %v, want export mode needs -out", err)
		}
		entries, err := os.ReadDir(filepath.Dir(cfg.Database))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("dir holds %d entries, want none", len(entries))
		}
	})

	t.Run("an existing file is refused and kept", func(t *testing.T) {
		cfg, out := exportPaths(t)
		writeTestFile(t, out, "keep")
		var log bytes.Buffer
		err := runExport(t.Context(), logtest.New(&log), cfg, out)
		if !errors.Is(err, fs.ErrExist) || !strings.HasPrefix(err.Error(), "creating export file: ") {
			t.Fatalf("runExport error = %v, want fs.ErrExist with the creating export file prefix", err)
		}
		if got := readTestFile(t, out); got != "keep" {
			t.Errorf("export file = %q, want it kept as %q", got, "keep")
		}
		if log.Len() != 0 {
			t.Errorf("a failed export logged:\n%s", log.String())
		}
	})

	t.Run("a failed store open creates no file", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{Database: filepath.Join(dir, "missing", "creator.db")}
		out := filepath.Join(dir, "export.jsonl")
		err := runExport(t.Context(), logtest.New(io.Discard), cfg, out)
		if !errors.Is(err, fs.ErrNotExist) || !strings.HasPrefix(err.Error(), "creating database file: ") {
			t.Fatalf("runExport error = %v, want fs.ErrNotExist with the creating database file prefix", err)
		}
		if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat of the export file = %v, want fs.ErrNotExist", err)
		}
	})
}

func TestExportFile_ErrorRemovesFile(t *testing.T) {
	_, out := exportPaths(t)
	errExport := errors.New("export failed")
	// The export writes a line before it fails, so the file holds a partial export.
	err := exportFile(t.Context(), out, func(_ context.Context, w io.Writer) error {
		_, _ = io.WriteString(w, exportLine)
		return errExport
	})
	if !errors.Is(err, errExport) {
		t.Fatalf("exportFile error = %v, want %v", err, errExport)
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat of the export file = %v, want fs.ErrNotExist", err)
	}
}
