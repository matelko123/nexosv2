package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticHandler serves the single-page app: existing files directly,
// everything else falls back to index.html for client-side routing.
func (s *Server) staticHandler() http.Handler {
	if s.cfg.Static == nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(s.cfg.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(s.cfg.Static, p); err != nil || st.IsDir() {
			p = "index.html"
		}
		if strings.HasPrefix(p, "assets/") {
			// Vite emits content-hashed file names under assets/.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if p == "index.html" {
			data, err := fs.ReadFile(s.cfg.Static, "index.html")
			if err != nil {
				http.Error(w, "UI not built", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + p
		files.ServeHTTP(w, r2)
	})
}
