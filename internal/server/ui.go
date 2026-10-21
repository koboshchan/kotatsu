package server

import (
	"embed"
	"net/http"
)

//go:embed web/*
var web embed.FS

func (s *Server) uiRoutes() {
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := web.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	s.mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		b, _ := web.ReadFile("web/app.js")
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(b)
	})
}
