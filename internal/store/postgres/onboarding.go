package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// ---- join tokens ----

type joinTokens struct{ s *Store }

const joinCols = `id, cluster, hash, created_by, created_at, expires_at, used_at, revoked_at`

func scanJoin(sc scanner) (store.JoinToken, error) {
	var (
		t                store.JoinToken
		created, expires int64
		used, revoked    sql.NullInt64
	)
	if err := sc.Scan(&t.ID, &t.Cluster, &t.Hash, &t.CreatedBy, &created, &expires, &used, &revoked); err != nil {
		return store.JoinToken{}, err
	}
	t.CreatedAt, t.ExpiresAt = storeutil.FromMs(created), storeutil.FromMs(expires)
	t.UsedAt, t.RevokedAt = fromNullMs(used), fromNullMs(revoked)
	return t, nil
}

// Create revokes the cluster's live tokens and inserts t in one
// transaction. The live-token UPDATE locks those rows, so two concurrent
// Creates for one cluster serialise and the loser's predecessor (the
// winner's token) is revoked too.
func (x joinTokens) Create(ctx context.Context, t store.JoinToken) error {
	t, err := storeutil.PrepareJoinToken(t)
	if err != nil {
		return err
	}
	return x.s.withTx(ctx, func(tx *sql.Tx) error {
		// Serialise Creates per cluster even when no live row exists yet.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('eddy-join:' || $1))`, t.Cluster); err != nil {
			return mapErr("lock join tokens", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE join_tokens SET revoked_at = $1
WHERE cluster = $2 AND used_at IS NULL AND revoked_at IS NULL`, storeutil.Ms(t.CreatedAt), t.Cluster); err != nil {
			return mapErr("revoke join tokens", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO join_tokens (`+joinCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			t.ID, t.Cluster, t.Hash, t.CreatedBy, storeutil.Ms(t.CreatedAt), storeutil.Ms(t.ExpiresAt), nullMs(t.UsedAt), nullMs(t.RevokedAt))
		return mapErr("create join token", err)
	})
}

func (x joinTokens) Get(ctx context.Context, id string) (store.JoinToken, error) {
	t, err := scanJoin(x.s.db.QueryRowContext(ctx, `SELECT `+joinCols+` FROM join_tokens WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.JoinToken{}, store.ErrNotFound
	}
	if err != nil {
		return store.JoinToken{}, mapErr("get join token", err)
	}
	return t, nil
}

// Consume is a single conditional UPDATE: the row lock makes exactly one
// concurrent caller match used_at IS NULL.
func (x joinTokens) Consume(ctx context.Context, id string, now time.Time) (store.JoinToken, error) {
	nowMs := storeutil.Ms(now)
	t, err := scanJoin(x.s.db.QueryRowContext(ctx, `UPDATE join_tokens SET used_at = $1
WHERE id = $2 AND used_at IS NULL AND revoked_at IS NULL AND expires_at > $1
RETURNING `+joinCols, nowMs, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.JoinToken{}, store.ErrNotFound
	}
	if err != nil {
		return store.JoinToken{}, mapErr("consume join token", err)
	}
	return t, nil
}

func (x joinTokens) List(ctx context.Context, cluster string) ([]store.JoinToken, error) {
	rows, err := x.s.db.QueryContext(ctx, `SELECT `+joinCols+` FROM join_tokens WHERE cluster = $1
ORDER BY created_at DESC, id DESC`, cluster)
	if err != nil {
		return nil, mapErr("list join tokens", err)
	}
	defer rows.Close()
	var out []store.JoinToken
	for rows.Next() {
		t, err := scanJoin(rows)
		if err != nil {
			return nil, mapErr("list join tokens", err)
		}
		t.Hash = nil
		out = append(out, t)
	}
	return out, mapErr("list join tokens", rows.Err())
}

func (x joinTokens) RevokeByCluster(ctx context.Context, cluster string, at time.Time) error {
	_, err := x.s.db.ExecContext(ctx, `UPDATE join_tokens SET revoked_at = $1
WHERE cluster = $2 AND used_at IS NULL AND revoked_at IS NULL`, storeutil.Ms(at), cluster)
	return mapErr("revoke join tokens", err)
}

// ---- connection attempts ----

type attempts struct{ s *Store }

// Record inserts and trims in one statement pair inside a transaction; the
// trim keeps the newest MaxAttemptsPerCluster rows of the cluster.
func (x attempts) Record(ctx context.Context, a store.ConnectionAttempt) error {
	a, err := storeutil.PrepareAttempt(a)
	if err != nil {
		return err
	}
	return x.s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO connection_attempts (cluster, at, reason, detail, peer, hub_pod)
VALUES ($1, $2, $3, $4, $5, $6)`, a.Cluster, storeutil.Ms(a.At), a.Reason, a.Detail, a.Peer, a.HubPod); err != nil {
			return mapErr("record connection attempt", err)
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM connection_attempts WHERE cluster = $1 AND id NOT IN (
  SELECT id FROM connection_attempts WHERE cluster = $1 ORDER BY at DESC, id DESC LIMIT `+strconv.Itoa(store.MaxAttemptsPerCluster)+`)`, a.Cluster)
		return mapErr("trim connection attempts", err)
	})
}

func (x attempts) List(ctx context.Context, cluster string) ([]store.ConnectionAttempt, error) {
	rows, err := x.s.db.QueryContext(ctx, `SELECT cluster, at, reason, detail, peer, hub_pod FROM connection_attempts
WHERE cluster = $1 ORDER BY at DESC, id DESC LIMIT `+strconv.Itoa(store.MaxAttemptsPerCluster), cluster)
	if err != nil {
		return nil, mapErr("list connection attempts", err)
	}
	defer rows.Close()
	out := []store.ConnectionAttempt{}
	for rows.Next() {
		var (
			a  store.ConnectionAttempt
			at int64
		)
		if err := rows.Scan(&a.Cluster, &at, &a.Reason, &a.Detail, &a.Peer, &a.HubPod); err != nil {
			return nil, mapErr("list connection attempts", err)
		}
		a.At = storeutil.FromMs(at)
		out = append(out, a)
	}
	return out, mapErr("list connection attempts", rows.Err())
}

func (x attempts) DeleteByCluster(ctx context.Context, cluster string) error {
	_, err := x.s.db.ExecContext(ctx, `DELETE FROM connection_attempts WHERE cluster = $1`, cluster)
	return mapErr("delete connection attempts", err)
}
