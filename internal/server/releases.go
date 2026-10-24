package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

var releaseFile = regexp.MustCompile(`^(kotatsu(-agent)?_v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?_(linux|darwin|windows)_(amd64|arm64)\.tar\.gz|SHA256SUMS|version\.json)$`)

func (s *Server) releaseRoutes() {
	s.mux.HandleFunc("GET /releases/{version}/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if s.Config.ReleaseDir == "" || r.PathValue("version") != s.Config.ReleaseVersion || !releaseFile.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(s.Config.ReleaseDir, name)
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		// No directory server, symlinks, hidden files, databases or token files.
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, path)
	})
	s.mux.HandleFunc("GET /{version}/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if s.Config.ReleaseDir == "" || r.PathValue("version") != s.Config.ReleaseVersion || !releaseFile.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(s.Config.ReleaseDir, name)
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		// No directory server, symlinks, hidden files, databases or token files.
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, path)
	})
}
