//go:build windows

package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadChromeVersionReadsResource(t *testing.T) {
	bin := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	raw, err := readChromeVersion(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseChromeVersion(raw); err != nil {
		t.Fatalf("readChromeVersion(%q) = %q: %v", bin, raw, err)
	}
}

func TestInstalledChromeReadsEnvBinary(t *testing.T) {
	bin := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	setChromePath(t, bin)
	want, err := chromeVersion(t.Context(), bin)
	if err != nil {
		t.Fatal(err)
	}
	got, err := InstalledChrome(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != (ChromeBinary{Path: bin, Version: want}) {
		t.Fatalf("InstalledChrome = %+v, want path %q and version %q", got, bin, want)
	}
}

func TestReadChromeVersionRejectsNonPE(t *testing.T) {
	notPE := filepath.Join(t.TempDir(), "chrome.exe")
	if err := os.WriteFile(notPE, []byte("not a PE image"), 0644); err != nil {
		t.Fatal(err)
	}
	if v, err := readChromeVersion(context.Background(), notPE); err == nil {
		t.Fatalf("readChromeVersion on a non-PE file = %q, want an error", v)
	}
}
