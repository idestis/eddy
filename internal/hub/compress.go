package hub

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// HTTP compression (ADR-0006). Cluster data compresses about 15–25× as
// JSON, so list responses and the SSE stream are gzipped when the browser
// accepts it.
//
// BREACH: compressing a response that carries a secret next to text an
// attacker can influence lets them guess the secret from the compressed
// length. compressible therefore allows only GET requests under /api/v1/
// and never the routes that carry secrets: /api/v1/me (the CSRF token),
// /api/v1/tokens (personal access tokens), join tokens and connection
// details, and pod logs (arbitrary workload output). /auth and /mcp are
// never wrapped at all.
const (
	gzipLevelBody   = 5
	gzipLevelStream = 1
	// gzipMinBytes leaves small bodies alone: gzip adds ~20 bytes and CPU.
	gzipMinBytes = 1024
)

func compressible(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/") {
		return false
	}
	if p == "/api/v1/me" || strings.HasPrefix(p, "/api/v1/tokens") || strings.HasPrefix(p, "/api/v1/ai/") {
		return false
	}
	for _, suffix := range []string{"/logs", "/connection", "/join-token"} {
		if strings.HasSuffix(p, suffix) {
			return false
		}
	}
	return true
}

// acceptsGzip reports whether Accept-Encoding lists gzip with a non-zero q.
func acceptsGzip(h http.Header) bool {
	for _, v := range h.Values("Accept-Encoding") {
		for part := range strings.SplitSeq(v, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
				continue
			}
			if q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q="); ok {
				if f, err := strconv.ParseFloat(q, 64); err == nil && f == 0 {
					return false
				}
			}
			return true
		}
	}
	return false
}

var gzipPools = map[int]*sync.Pool{
	gzipLevelBody:   {New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzipLevelBody); return w }},
	gzipLevelStream: {New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzipLevelStream); return w }},
}

// gzipResponses compresses the responses of compressible requests.
func gzipResponses(m *metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !compressible(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r.Header) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w, metrics: m}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

// gzipWriter buffers the first gzipMinBytes of a body to decide whether to
// compress it. An event stream is compressed at once at level 1, and every
// Flush flushes the compressor so each event reaches the browser.
type gzipWriter struct {
	http.ResponseWriter
	metrics *metrics

	status  int
	decided bool
	buf     []byte
	gz      *gzip.Writer
	level   int
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) WriteHeader(code int) {
	if g.status != 0 {
		return
	}
	g.status = code
	h := g.Header()
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified || h.Get("Content-Encoding") != "" {
		g.decide(false)
		return
	}
	if strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") {
		g.level = gzipLevelStream
		g.decide(true)
	}
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if g.status == 0 {
		g.WriteHeader(http.StatusOK)
	}
	if !g.decided {
		g.buf = append(g.buf, b...)
		if len(g.buf) >= gzipMinBytes {
			g.decide(true)
		}
		return len(b), nil
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// decide commits the headers and writes the buffered bytes.
func (g *gzipWriter) decide(compress bool) {
	if g.decided {
		return
	}
	g.decided = true
	if compress {
		h := g.Header()
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		if g.level == 0 {
			g.level = gzipLevelBody
		}
		g.gz = gzipPools[g.level].Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
		g.metrics.gzipResponses.Add(1)
	}
	g.ResponseWriter.WriteHeader(g.status)
	if len(g.buf) > 0 {
		if g.gz != nil {
			_, _ = g.gz.Write(g.buf)
		} else {
			_, _ = g.ResponseWriter.Write(g.buf)
		}
	}
	g.buf = nil
}

// Flush sends what is buffered: it commits a small body uncompressed, and
// flushes the compressor of a compressed one.
func (g *gzipWriter) Flush() {
	if g.status == 0 {
		g.WriteHeader(http.StatusOK)
	}
	g.decide(len(g.buf) >= gzipMinBytes)
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) finish() {
	if g.status == 0 {
		return // nothing was written; the server sends an empty 200
	}
	g.decide(len(g.buf) >= gzipMinBytes)
	if g.gz != nil {
		_ = g.gz.Close()
		g.gz.Reset(io.Discard)
		gzipPools[g.level].Put(g.gz)
		g.gz = nil
	}
}
