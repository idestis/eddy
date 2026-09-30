// Package ui embeds the built single-page app (web/, built by `task ui`
// into internal/ui/dist) and serves it.
//
// Hashed files under /assets/ are cached for a year as immutable. Every
// other path that is not a file in the build falls back to index.html with
// Cache-Control: no-store, so client-side routes deep-link. Paths under
// /api, /auth and /mcp are never answered with the SPA. When the build is
// missing (a fresh checkout), a short plain-text page explains how to build
// it.
package ui

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// notBuilt is served when dist holds no index.html.
const notBuilt = `Eddy: the web UI is not built into this binary.

Build it with:

    task ui        # pnpm install && pnpm build in web/, output in internal/ui/dist

then rebuild or re-run the hub. For UI development, run "task ui:dev" and open
the Vite dev server instead; it proxies /api, /auth and /mcp to this hub.
`

// Handler serves the embedded SPA.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub only fails on an invalid path, and "dist" is valid.
		return http.NotFoundHandler()
	}
	return NewHandler(sub)
}

// NewHandler serves an SPA build from fsys (the contents of dist).
func NewHandler(fsys fs.FS) http.Handler {
	index, err := fs.ReadFile(fsys, "index.html")
	h := &handler{fsys: fsys}
	if err == nil {
		h.index = index
	}
	return h
}

type handler struct {
	fsys  fs.FS
	index []byte // nil when the SPA is not built
}

// reserved are the server's own prefixes; the SPA never answers them.
var reserved = []string{"/api", "/auth", "/mcp"}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	for _, pre := range reserved {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			http.NotFound(w, r)
			return
		}
	}
	if h.index == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, notBuilt)
		return
	}
	name := strings.TrimPrefix(p, "/")
	if strings.HasPrefix(p, "/assets/") {
		// Hashed build output: a missing asset is a real 404, never index.html.
		if !h.serveFile(w, r, name, "public, max-age=31536000, immutable") {
			http.NotFound(w, r)
		}
		return
	}
	if name != "" && name != "index.html" && h.serveFile(w, r, name, "no-cache") {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

// serveFile serves a regular file from the build, reporting whether it existed.
func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name, cacheControl string) bool {
	if strings.HasPrefix(path.Base(name), ".") {
		return false // .gitkeep and other dotfiles are not part of the app
	}
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		rs = bytes.NewReader(b)
	}
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, name, st.ModTime(), rs)
	return true
}

// Built reports whether the embedded build contains index.html.
func Built() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}
