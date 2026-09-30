// Package audit records who did what, through which channel, to which
// target. Every event goes to the store and, as a JSON log line, to stdout so
// log shipping remains the long-term audit trail.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// maxDetail caps the serialised detail stored per event.
const maxDetail = 4 << 10

// Recorder writes audit events. The zero value is not usable; use New.
type Recorder struct {
	st  store.Audit
	log *slog.Logger
	now func() time.Time
}

// New returns a Recorder. st may be nil (log only).
func New(st store.Audit, log *slog.Logger) *Recorder {
	return &Recorder{st: st, log: log.With("log", "audit"), now: time.Now}
}

// Record writes one event. Detail must not contain secrets; it is truncated
// to 4 KiB. Store errors are logged, never returned: auditing must not turn
// a completed action into a failed request.
func (r *Recorder) Record(ctx context.Context, p identity.Principal, action string, target store.ResourceRef, result store.AuditResult, detail any) {
	var raw json.RawMessage
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			if len(b) > maxDetail {
				b, _ = json.Marshal(map[string]any{"truncated": true, "bytes": len(b)})
			}
			raw = b
		}
	}
	e := store.AuditEvent{
		Time:      r.now().UTC(),
		RequestID: RequestID(ctx),
		Subject:   p.User,
		Groups:    p.Groups,
		Via:       string(p.Via),
		TokenID:   p.TokenID,
		Action:    action,
		Target:    target,
		Result:    result,
		Detail:    raw,
	}
	r.log.LogAttrs(ctx, slog.LevelInfo, "audit",
		slog.String("action", action),
		slog.String("user", p.User),
		slog.Any("groups", p.Groups),
		slog.String("via", string(p.Via)),
		slog.String("tokenId", p.TokenID),
		slog.String("client", p.Client),
		slog.String("cluster", target.Cluster),
		slog.String("kind", target.Kind),
		slog.String("namespace", target.Namespace),
		slog.String("name", target.Name),
		slog.String("result", string(result)),
		slog.String("requestId", e.RequestID),
		slog.Any("detail", raw),
	)
	if r.st == nil {
		return
	}
	if err := r.st.Append(context.WithoutCancel(ctx), e); err != nil {
		r.log.ErrorContext(ctx, "audit store append failed", "err", err, "action", action)
	}
}

type reqIDKey struct{}

// WithRequestID stores a request id in ctx for correlation.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, reqIDKey{}, id)
}

// RequestID returns the request id in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(reqIDKey{}).(string)
	return id
}
