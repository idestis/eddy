package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPA(t *testing.T) {
	h := NewHandler(fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>Eddy</title>")},
		"assets/index-abc.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc.css": {Data: []byte("body{}")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		".gitkeep":             {Data: nil},
	})
	tests := []struct {
		method, path string
		status       int
		cache        string
		body         string
	}{
		{"GET", "/", 200, "no-store", "<title>Eddy"},
		{"GET", "/index.html", 200, "no-store", "<title>Eddy"},
		{"GET", "/c/prod/r/Kustomization/flux-system/apps", 200, "no-store", "<title>Eddy"},
		{"GET", "/assets/index-abc.js", 200, "public, max-age=31536000, immutable", "console.log"},
		{"HEAD", "/assets/index-abc.css", 200, "public, max-age=31536000, immutable", ""},
		{"GET", "/assets/missing.js", 404, "", ""},
		{"GET", "/favicon.svg", 200, "no-cache", "<svg/>"},
		{"GET", "/.gitkeep", 200, "no-store", "<title>Eddy"},
		{"GET", "/api/v1/nope", 404, "", ""},
		{"GET", "/auth", 404, "", ""},
		{"GET", "/mcp/x", 404, "", ""},
		{"GET", "/../../etc/passwd", 200, "no-store", "<title>Eddy"},
		{"POST", "/", 405, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/", nil)
			req.URL.Path = tc.path
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			if tc.cache != "" && rec.Header().Get("Cache-Control") != tc.cache {
				t.Fatalf("Cache-Control %q, want %q", rec.Header().Get("Cache-Control"), tc.cache)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("body %q lacks %q", rec.Body.String(), tc.body)
			}
		})
	}
}

func TestNotBuilt(t *testing.T) {
	h := NewHandler(fstest.MapFS{".gitkeep": {}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "task ui") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("not-built page: %d %q", rec.Code, rec.Body.String())
	}
}

func TestEmbeddedHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("embedded handler: %d", rec.Code)
	}
}
