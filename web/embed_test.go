package web

import (
	"bytes"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

const builtIndex = "<!doctype html><title>dashboard</title>"

func TestSPAOf(t *testing.T) {
	t.Run("a built tree", func(t *testing.T) {
		got, err := spaOf(fstest.MapFS{
			"dist/.gitkeep":        {},
			"dist/spa/index.html":  {Data: []byte(builtIndex)},
			"dist/spa/_app/a.js":   {Data: []byte("console.log(1)")},
			"dist/other/stray.txt": {},
		})
		if err != nil {
			t.Fatalf("spaOf() error = %v, want nil", err)
		}
		if data, err := fs.ReadFile(got, "index.html"); err != nil || string(data) != builtIndex {
			t.Errorf("index.html = %q, %v, want %q", data, err, builtIndex)
		}
		if _, err := fs.Stat(got, "_app/a.js"); err != nil {
			t.Errorf("_app/a.js: %v, want the file", err)
		}
		if _, err := fs.Stat(got, ".gitkeep"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf(".gitkeep: %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("a tree with only the .gitkeep", func(t *testing.T) {
		got, err := spaOf(fstest.MapFS{"dist/.gitkeep": {}})

		const want = "reading the dashboard: open index.html: file does not exist (build it with npm --prefix web run build)"
		if got != nil || !errors.Is(err, fs.ErrNotExist) || err.Error() != want {
			t.Errorf("spaOf() = %v, %v, want no FS and %q", got, err, want)
		}
	})
}

// A clean clone embeds only the .gitkeep, and a tree with a build embeds dist/spa.
// The test holds in both: SPA must serve the embedded index.html when there is one.
func TestSPA(t *testing.T) {
	want, statErr := fs.ReadFile(dist, "dist/spa/index.html")

	got, err := SPA()

	if statErr != nil {
		if got != nil || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("SPA() with no build = %v, %v, want no FS and fs.ErrNotExist", got, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("SPA() with a build: %v, want nil", err)
	}
	if data, err := fs.ReadFile(got, "index.html"); err != nil || !bytes.Equal(data, want) {
		t.Errorf("index.html = %d bytes, %v, want the %d embedded bytes", len(data), err, len(want))
	}
}
