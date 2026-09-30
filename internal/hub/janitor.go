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
	optimizeEvery   = 24 * time.Hour
	tokenPurgeAfter = 30 * 24 * time.Hour
)

// optimizer is implemented by backends with a maintenance pass (sqlite).
type optimizer interface {
	Optimize(ctx context.Context) error
}

func retention(r config.Retention) store.Retention {
	return store.Retention{
		AuditDays:           r.AuditDays,
		ResolvedThreadsDays: r.ResolvedThreadsDays,
		AskThreadsDays:      r.AskThreadsDays,
		TokenPurgeAfter:     tokenPurgeAfter,
	}
}

// runJanitor prunes expired data every 10 minutes and optimizes the
// database daily, until ctx is done.
func runJanitor(ctx context.Context, st store.Store, r config.Retention, log *slog.Logger) {
	log = log.With("component", "janitor")
	ret := retention(r)
	var lastOptimize time.Time
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
				"audit", stats.Audit, "threads", stats.Threads)
		}
		if o, ok := st.(optimizer); ok && now.Sub(lastOptimize) >= optimizeEvery {
			octx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			if err := o.Optimize(octx); err != nil && ctx.Err() == nil {
				log.Warn("optimize failed", "err", err)
			} else {
				lastOptimize = now
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
