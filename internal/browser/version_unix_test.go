//go:build !windows

package browser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledChromeReadsEnvBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'Google Chrome 155.0.8059.40'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	setChromePath(t, bin)
	got, err := InstalledChrome(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := (ChromeBinary{Path: bin, Version: "155.0.8059.40"}); got != want {
		t.Fatalf("InstalledChrome = %+v, want %+v", got, want)
	}
}
