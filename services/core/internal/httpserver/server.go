package httpserver

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// New exposes process liveness and the static console. It does not expose business APIs.
func New(assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		// A disconnected liveness client needs no retry or second response.
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "API not found", http.StatusNotFound)
	})
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "API not found", http.StatusNotFound)
	})
	files := http.FileServerFS(assets)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		}
		info, err := fs.Stat(assets, name)
		if err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "Asset lookup failed", http.StatusInternalServerError)
			return
		}
		// Only HTML navigation may fall back to the SPA; assets and API failures stay failures.
		if strings.HasPrefix(name, "assets/") || name == "assets" || path.Ext(name) != "" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		request := r.Clone(r.Context())
		request.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, request)
	})
	return mux
}
