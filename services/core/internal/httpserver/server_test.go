package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRoutingBoundaries(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":    {Data: []byte("<html>console</html>")},
		"assets/app.js": {Data: []byte("console.log('app')")},
	}
	tests := []struct {
		name, method, path, accept string
		status                     int
		body                       string
	}{
		{"liveness", "GET", "/api/health", "application/json", 200, `{"status":"ok"}`},
		{"home", "GET", "/", "text/html", 200, "<html>console</html>"},
		{"deep route", "GET", "/skills/example", "text/html", 200, "<html>console</html>"},
		{"asset", "GET", "/assets/app.js", "*/*", 200, "console.log"},
		{"unknown API", "GET", "/api/missing", "text/html", 404, "API not found"},
		{"API root", "GET", "/api", "text/html", 404, "API not found"},
		{"missing asset", "GET", "/assets/missing.js", "text/html", 404, "404"},
		{"asset directory", "GET", "/assets", "text/html", 404, "404"},
		{"non HTML", "GET", "/unknown", "application/json", 404, "404"},
		{"missing stylesheet", "GET", "/missing.css", "text/html", 404, "404"},
		{"reject write", "POST", "/", "text/html", 405, "Method Not Allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, tt.path, nil)
			request.Header.Set("Accept", tt.accept)
			response := httptest.NewRecorder()
			New(assets).ServeHTTP(response, request)
			if response.Code != tt.status || !strings.Contains(response.Body.String(), tt.body) {
				t.Fatalf("got %d %q, want %d containing %q", response.Code, response.Body.String(), tt.status, tt.body)
			}
		})
	}
}
