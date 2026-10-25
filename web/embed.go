// Package web holds the built dashboard of serve mode.
package web

import (
	"embed"
	"fmt"
	"io/fs"
)

// dist holds the build output in dist/spa. The all: prefix keeps the _app dir
// and the tracked .gitkeep, so a clean clone builds with no dashboard.
//
//go:embed all:dist
var dist embed.FS

// SPA returns the built dashboard, the dist/spa sub-tree. It returns an error
// when that tree has no index.html.
func SPA() (fs.FS, error) {
	return spaOf(dist)
}

// spaOf returns the dist/spa sub-tree of fsys, or an error when it has no index.html.
func spaOf(fsys fs.FS) (fs.FS, error) {
	// Sub fails only on an invalid dir name, and the name is a constant.
	sub, _ := fs.Sub(fsys, "dist/spa")
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, fmt.Errorf("reading the dashboard: %w (build it with npm --prefix web run build)", err)
	}
	return sub, nil
}
