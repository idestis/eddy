package hub

import (
	"context"
	"log/slog"
	"time"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/store"
)

// Janitor cadence (ADR-0002 §3).
const (
	janitorEvery    = 10 * time.Minute
	tokenPurgeAfter = 30 * 24 * time.Hour
)

func retention(r config.Retention) store.Retention {
	return store.Retention{
		AuditDays:           r.AuditDays,
		ResolvedThreadsDays: r.ResolvedThreadsDays,
		ChatDays:            r.ChatDays,
		TokenPurgeAfter:     tokenPurgeAfter,
	}
}

// runJanitor prunes expired data every 10 minutes until ctx is done. Every
// replica runs it; the deletes are idempotent, so running them concurrently
// is harmless.
func runJanitor(ctx context.Context, st store.Store, r config.Retention, log *slog.Logger) {
	log = log.With("component", "janitor")
	ret := retention(r)
	t := time.NewTicker(janitorEvery)
	defer t.Stop()
	for {
		now := time.Now()
		pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		stats, err := st.Prune(pctx, now, ret)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("prune failed", "err", err)
		case err == nil && stats != (store.PruneStats{}):
			log.Info("pruned expired data", "sessions", stats.Sessions, "tokens", stats.Tokens,
				"audit", stats.Audit, "threads", stats.Threads, "chats", stats.Chats, "rateLimits", stats.RateLimits, "agentSessions", stats.AgentSessions)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
