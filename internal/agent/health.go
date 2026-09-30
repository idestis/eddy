package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// healthHandler serves /healthz (the process is up) and /readyz. Readiness
// requires synced informers; the hub connection state is reported but does
// not gate readiness, since nothing routes traffic to the agent and a hub
// outage should not restart it.
func healthHandler(synced, connected func() bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		body := struct {
			Synced    bool `json:"synced"`
			Connected bool `json:"connected"`
		}{synced(), connected()}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !body.Synced {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	return mux
}

// serveHealth runs the health server on addr until ctx is done.
func serveHealth(ctx context.Context, addr string, synced, connected func() bool) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           healthHandler(synced, connected),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("agent: health server: %w", err)
	}
	return nil
}
