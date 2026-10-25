package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestColorTerminalFalseForFileAndNull(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "run.log"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = file.Close() }()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = null.Close() }()

	for _, f := range []*os.File{file, null} {
		if colorTerminal(f) {
			t.Errorf("colorTerminal(%s) = true, want false", f.Name())
		}
	}
}
