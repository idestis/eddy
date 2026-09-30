package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// ---- rate limits ----

type rateLimits struct{ s *Store }

// Hit is one upsert: it either starts a window (count 1) or increments the
// open one. ON CONFLICT DO UPDATE locks the row, and the SET expressions
// read the latest committed row, so concurrent hits from every replica are
// counted exactly.
func (x rateLimits) Hit(ctx context.Context, key string, window time.Duration, limit int, now time.Time) (int, bool, error) {
	if err := storeutil.CheckRateLimit(key, window); err != nil {
		return 0, false, err
	}
	nowMs := storeutil.Ms(now)
	var count int
	err := x.s.db.QueryRowContext(ctx, `INSERT INTO rate_limits AS r (key, window_start, reset_at, count)
VALUES ($1, $2, $3, 1)
ON CONFLICT (key) DO UPDATE SET
  count        = CASE WHEN r.reset_at > $2 THEN r.count + 1 ELSE 1 END,
  window_start = CASE WHEN r.reset_at > $2 THEN r.window_start ELSE $2 END,
  reset_at     = CASE WHEN r.reset_at > $2 THEN r.reset_at ELSE $3 END
RETURNING count`, key, nowMs, nowMs+window.Milliseconds()).Scan(&count)
	if err != nil {
		return 0, false, mapErr("rate limit hit", err)
	}
	return count, limit <= 0 || count <= limit, nil
}

func (x rateLimits) Get(ctx context.Context, key string, now time.Time) (int, time.Time, error) {
	var (
		count int
		reset int64
	)
	err := x.s.db.QueryRowContext(ctx, `SELECT count, reset_at FROM rate_limits WHERE key = $1 AND reset_at > $2`,
		key, storeutil.Ms(now)).Scan(&count, &reset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, nil
	}
	if err != nil {
		return 0, time.Time{}, mapErr("rate limit get", err)
	}
	return count, storeutil.FromMs(reset), nil
}

func (x rateLimits) Reset(ctx context.Context, key string) error {
	_, err := x.s.db.ExecContext(ctx, `DELETE FROM rate_limits WHERE key = $1`, key)
	return mapErr("rate limit reset", err)
}

// ---- agent sessions ----

type agentSessions struct{ s *Store }

const agentCols = `cluster, agent_instance, hub_pod, hub_addr, seq, connected_at, heartbeat_at`

// Upsert replaces the instance's row only when the new dial is newer (or is
// a refresh from the same replica); the condition is part of the upsert, so
// two replicas racing for one instance cannot both win.
func (x agentSessions) Upsert(ctx context.Context, v store.AgentSession) error {
	v, err := storeutil.PrepareAgentSession(v)
	if err != nil {
		return err
	}
	res, err := x.s.db.ExecContext(ctx, `INSERT INTO agent_sessions AS a (`+agentCols+`)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (cluster, agent_instance) DO UPDATE SET
  hub_pod = excluded.hub_pod, hub_addr = excluded.hub_addr, seq = excluded.seq,
  connected_at = excluded.connected_at, heartbeat_at = excluded.heartbeat_at
WHERE excluded.seq > a.seq OR (excluded.seq = a.seq AND excluded.hub_pod = a.hub_pod)`,
		v.Cluster, v.AgentInstance, v.HubPod, v.HubAddr, v.Seq, storeutil.Ms(v.ConnectedAt), storeutil.Ms(v.HeartbeatAt))
	if err != nil {
		return mapErr("upsert agent session", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr("upsert agent session", err)
	}
	if n == 0 {
		return store.ErrConflict
	}
	return nil
}

func (x agentSessions) Heartbeat(ctx context.Context, cluster, hubPod, agentInstance string, at time.Time) error {
	res, err := x.s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET heartbeat_at = $1 WHERE cluster = $2 AND agent_instance = $3 AND hub_pod = $4`,
		storeutil.Ms(at), cluster, agentInstance, hubPod)
	return affectedOne(res, err, "agent session heartbeat")
}

func (x agentSessions) Delete(ctx context.Context, cluster, hubPod, agentInstance string) error {
	_, err := x.s.db.ExecContext(ctx,
		`DELETE FROM agent_sessions WHERE cluster = $1 AND agent_instance = $2 AND hub_pod = $3`,
		cluster, agentInstance, hubPod)
	return mapErr("delete agent session", err)
}

func (x agentSessions) List(ctx context.Context, cluster string, freshAfter time.Time) ([]store.AgentSession, error) {
	rows, err := x.s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agent_sessions
WHERE ($1 = '' OR cluster = $1) AND heartbeat_at > $2
ORDER BY connected_at, hub_pod, agent_instance, cluster`, cluster, storeutil.Ms(freshAfter))
	if err != nil {
		return nil, mapErr("list agent sessions", err)
	}
	defer rows.Close()
	var out []store.AgentSession
	for rows.Next() {
		var (
			v               store.AgentSession
			connected, beat int64
		)
		if err := rows.Scan(&v.Cluster, &v.AgentInstance, &v.HubPod, &v.HubAddr, &v.Seq, &connected, &beat); err != nil {
			return nil, mapErr("list agent sessions", err)
		}
		v.ConnectedAt, v.HeartbeatAt = storeutil.FromMs(connected), storeutil.FromMs(beat)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr("list agent sessions", err)
	}
	return out, nil
}

func (x agentSessions) DeleteByHub(ctx context.Context, hubPod string) error {
	_, err := x.s.db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE hub_pod = $1`, hubPod)
	return mapErr("delete agent sessions by hub", err)
}
