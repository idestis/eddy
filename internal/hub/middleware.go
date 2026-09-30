package hub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/eddy-gitops/eddy/internal/audit"
	"github.com/eddy-gitops/eddy/internal/identity"
)

// Request body caps.
const (
	maxBodyBytes     = 1 << 20   // any request on the UI listener
	maxJSONBodyBytes = 64 << 10  // most JSON bodies
	maxThreadBody    = 512 << 10 // thread bodies: 64 KiB of text, JSON-escaped
)

// statusRecorder captures the status code and size for the access log and
// metrics. It keeps Flush and the ResponseController working for SSE.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Flush() {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// observe assigns a request id, recovers panics, caps the body, and writes
// one access log line (method, path, status, size, duration, user; never
// headers, cookies or query strings) and the request metric.
func observe(log *slog.Logger, m *metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		ctx := audit.WithRequestID(r.Context(), id)
		// The principal is set further down the chain; record it on the way out.
		var user string
		ctx = withUserSlot(ctx, &user)
		r = r.WithContext(ctx)
		if r.ContentLength > maxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "bad_request", "request body too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.ErrorContext(ctx, "panic serving request", "panic", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				if rec.status == 0 {
					writeError(rec, http.StatusInternalServerError, "internal", "internal error")
				}
			}
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			m.request(status)
			log.LogAttrs(ctx, slog.LevelInfo, "http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("bytes", rec.bytes),
				slog.Float64("durationMs", float64(time.Since(start).Microseconds())/1000),
				slog.String("user", user),
				slog.String("requestId", id),
			)
		}()
		next.ServeHTTP(rec, r)
	})
}

// recordUser copies the authenticated user into the access-log slot.
func recordUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := identity.From(r.Context()); ok {
			if slot := userSlot(r.Context()); slot != nil {
				*slot = p.User
			}
		}
		next.ServeHTTP(w, r)
	})
}

// recoverPlain recovers panics on the agent and metrics listeners.
func recoverPlain(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("panic serving request", "panic", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// isMaxBytes reports whether err comes from http.MaxBytesReader.
func isMaxBytes(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
