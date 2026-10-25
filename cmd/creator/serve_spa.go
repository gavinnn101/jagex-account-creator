package main

import (
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler serves the dashboard files of fsys. A path that names a file gets
// the file. A missing path under /_app/ gets 404, because the app asks there
// only for files of its build. Any other path is a client route and gets
// index.html. A path that ends in /index.html gets the 301 of http.FileServer
// to ./, so the app shell has one URL.
func spaHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The mux has cleaned the path, so the name holds no ".." element.
		name := strings.TrimPrefix(r.URL.Path, "/")
		switch {
		case isFile(fsys, name):
			files.ServeHTTP(w, r)
		case strings.HasPrefix(name, "_app/"):
			http.NotFound(w, r)
		default:
			http.ServeFileFS(w, r, fsys, "index.html")
		}
	})
}

// isFile reports whether name is a regular file of fsys. A dir is not one, so
// the file server never lists a dir.
func isFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.Mode().IsRegular()
}

// unknownEndpoint answers a GET of an API path that no route has, with the JSON 404.
func (s *apiServer) unknownEndpoint(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, http.StatusNotFound, "no API endpoint at "+r.URL.Path)
}
