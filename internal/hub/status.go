package hub

import (
	"context"
	"log/slog"
	"time"
)

// Status writing cadence: connection changes are written at once, other
// changes (lastSeen, resource counts) at most every statusMinInterval.
const (
	statusTick        = 5 * time.Second
	statusMinInterval = 30 * time.Second
)

// statusWriter mirrors connection state into Cluster status. It pulls the
// desired state from current, so a missed kick only delays a write.
type statusWriter struct {
	current func() map[string]clusterStatus
	patch   func(ctx context.Context, name string, st clusterStatus) error
	log     *slog.Logger
	kickCh  chan struct{}
	now     func() time.Time

	sent map[string]sentStatus
}

type sentStatus struct {
	st clusterStatus
	at time.Time
}

func newStatusWriter(current func() map[string]clusterStatus, patch func(context.Context, string, clusterStatus) error, log *slog.Logger) *statusWriter {
	return &statusWriter{
		current: current,
		patch:   patch,
		log:     log.With("component", "status"),
		kickCh:  make(chan struct{}, 1),
		now:     time.Now,
		sent:    map[string]sentStatus{},
	}
}

// kick asks for an immediate pass, for example after a connect.
func (w *statusWriter) kick() {
	select {
	case w.kickCh <- struct{}{}:
	default:
	}
}

func (w *statusWriter) Run(ctx context.Context) {
	t := time.NewTicker(statusTick)
	defer t.Stop()
	for {
		w.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.kickCh:
		}
	}
}

func (w *statusWriter) pass(ctx context.Context) {
	now := w.now()
	want := w.current()
	for name, st := range want {
		prev, ok := w.sent[name]
		if ok && prev.st == st {
			continue
		}
		if ok && prev.st.Phase == st.Phase && now.Sub(prev.at) < statusMinInterval {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := w.patch(pctx, name, st)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				w.log.Warn("cluster status update failed", "cluster", name, "err", err)
			}
			continue
		}
		w.sent[name] = sentStatus{st: st, at: now}
	}
	for name := range w.sent {
		if _, ok := want[name]; !ok {
			delete(w.sent, name)
		}
	}
}
