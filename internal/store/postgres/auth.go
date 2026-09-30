package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

func encodeStrings(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("postgres: encode list: %w", err)
	}
	return string(b), nil
}

func decodeStrings(s string) ([]string, error) {
	var v []string
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("postgres: decode list: %w", err)
	}
	return storeutil.Strings(v), nil
}

// ---- sessions ----

type sessions struct{ s *Store }

const sessionCols = `id_hash, subject, display, groups, provider, created_at, last_seen_at, expires_at, user_agent, remote_ip`

func (x sessions) Create(ctx context.Context, v store.Session) error {
	v, err := storeutil.PrepareSession(v)
	if err != nil {
		return err
	}
	groups, err := encodeStrings(v.Groups)
	if err != nil {
		return err
	}
	_, err = x.s.db.ExecContext(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		v.IDHash, v.Subject, v.Display, groups, v.Provider,
		storeutil.Ms(v.CreatedAt), storeutil.Ms(v.LastSeenAt), storeutil.Ms(v.ExpiresAt), v.UserAgent, v.RemoteIP)
	return mapErr("create session", err)
}

func (x sessions) Get(ctx context.Context, idHash []byte, now time.Time) (store.Session, error) {
	row := x.s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id_hash = $1 AND expires_at > $2`,
		idHash, storeutil.Ms(now))
	var (
		v                        store.Session
		groups                   string
		created, seen, expiresMs int64
	)
	err := row.Scan(&v.IDHash, &v.Subject, &v.Display, &groups, &v.Provider, &created, &seen, &expiresMs, &v.UserAgent, &v.RemoteIP)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Session{}, store.ErrNotFound
	}
	if err != nil {
		return store.Session{}, mapErr("get session", err)
	}
	if v.Groups, err = decodeStrings(groups); err != nil {
		return store.Session{}, err
	}
	v.CreatedAt, v.LastSeenAt, v.ExpiresAt = storeutil.FromMs(created), storeutil.FromMs(seen), storeutil.FromMs(expiresMs)
	return v, nil
}

func (x sessions) Touch(ctx context.Context, idHash []byte, seen, expires time.Time) error {
	res, err := x.s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = $1, expires_at = $2 WHERE id_hash = $3`,
		storeutil.Ms(seen), storeutil.Ms(expires), idHash)
	return affectedOne(res, err, "touch session")
}

func (x sessions) Delete(ctx context.Context, idHash []byte) error {
	_, err := x.s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = $1`, idHash)
	return mapErr("delete session", err)
}

func (x sessions) DeleteBySubject(ctx context.Context, subject string) error {
	_, err := x.s.db.ExecContext(ctx, `DELETE FROM sessions WHERE subject = $1`, subject)
	return mapErr("delete sessions by subject", err)
}

func (x sessions) DeleteOldestBySubject(ctx context.Context, subject string, keep int) error {
	_, err := x.s.db.ExecContext(ctx, `DELETE FROM sessions WHERE subject = $1 AND id_hash NOT IN (
		SELECT id_hash FROM sessions WHERE subject = $1
		ORDER BY last_seen_at DESC, created_at DESC, id_hash DESC LIMIT $2)`, subject, max(keep, 0))
	return mapErr("trim sessions", err)
}

func (x sessions) LatestGroups(ctx context.Context, subject string) ([]string, error) {
	var groups string
	err := x.s.db.QueryRowContext(ctx,
		`SELECT groups FROM sessions WHERE subject = $1 ORDER BY last_seen_at DESC, created_at DESC, id_hash DESC LIMIT 1`,
		subject).Scan(&groups)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, mapErr("latest session groups", err)
	}
	return decodeStrings(groups)
}

// ---- tokens ----

type tokens struct{ s *Store }

const tokenCols = `id, hash, subject, display, provider, groups, name, scopes, created_at, expires_at, last_used_at, revoked_at`

// Create inserts the token. With a positive maxActive it counts the
// subject's active tokens first, in the same transaction and under
// pg_advisory_xact_lock(hashtext(subject)), so concurrent Creates for one
// subject on any replica cannot exceed the cap.
func (x tokens) Create(ctx context.Context, t store.Token, maxActive int) error {
	t, err := storeutil.PrepareToken(t)
	if err != nil {
		return err
	}
	groups, err := encodeStrings(t.Groups)
	if err != nil {
		return err
	}
	scopes, err := encodeStrings(t.Scopes)
	if err != nil {
		return err
	}
	insert := func(ex interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}) error {
		_, err := ex.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			t.ID, t.Hash, t.Subject, t.Display, t.Provider, groups, t.Name, scopes,
			storeutil.Ms(t.CreatedAt), storeutil.Ms(t.ExpiresAt), nullMs(t.LastUsedAt), nullMs(t.RevokedAt))
		return mapErr("create token", err)
	}
	if maxActive <= 0 {
		return insert(x.s.db)
	}
	return x.s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, t.Subject); err != nil {
			return mapErr("lock token subject", err)
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM api_tokens WHERE subject = $1 AND revoked_at IS NULL AND expires_at > $2`,
			t.Subject, storeutil.Ms(t.CreatedAt)).Scan(&n); err != nil {
			return mapErr("count tokens", err)
		}
		if n >= maxActive {
			return fmt.Errorf("postgres: create token: %w: %d active tokens", store.ErrLimit, n)
		}
		return insert(tx)
	})
}

func scanToken(sc scanner) (store.Token, error) {
	var (
		t                store.Token
		groups, scopes   string
		created, expires int64
		used, revoked    sql.NullInt64
	)
	if err := sc.Scan(&t.ID, &t.Hash, &t.Subject, &t.Display, &t.Provider, &groups, &t.Name, &scopes,
		&created, &expires, &used, &revoked); err != nil {
		return store.Token{}, err
	}
	var err error
	if t.Groups, err = decodeStrings(groups); err != nil {
		return store.Token{}, err
	}
	if t.Scopes, err = decodeStrings(scopes); err != nil {
		return store.Token{}, err
	}
	t.CreatedAt, t.ExpiresAt = storeutil.FromMs(created), storeutil.FromMs(expires)
	t.LastUsedAt, t.RevokedAt = fromNullMs(used), fromNullMs(revoked)
	return t, nil
}

func (x tokens) Get(ctx context.Context, id string, now time.Time) (store.Token, error) {
	t, err := scanToken(x.s.db.QueryRowContext(ctx,
		`SELECT `+tokenCols+` FROM api_tokens WHERE id = $1 AND revoked_at IS NULL AND expires_at > $2`,
		id, storeutil.Ms(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Token{}, store.ErrNotFound
	}
	if err != nil {
		return store.Token{}, mapErr("get token", err)
	}
	return t, nil
}

func (x tokens) List(ctx context.Context, subject string) ([]store.Token, error) {
	rows, err := x.s.db.QueryContext(ctx,
		`SELECT `+tokenCols+` FROM api_tokens WHERE subject = $1 ORDER BY created_at DESC, id DESC`, subject)
	if err != nil {
		return nil, mapErr("list tokens", err)
	}
	defer rows.Close()
	var out []store.Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, mapErr("list tokens", err)
		}
		t.Hash = nil // List never returns hashes.
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr("list tokens", err)
	}
	return out, nil
}

func (x tokens) CountActive(ctx context.Context, subject string, now time.Time) (int, error) {
	var n int
	if err := x.s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM api_tokens WHERE subject = $1 AND revoked_at IS NULL AND expires_at > $2`,
		subject, storeutil.Ms(now)).Scan(&n); err != nil {
		return 0, mapErr("count tokens", err)
	}
	return n, nil
}

func (x tokens) Revoke(ctx context.Context, subject, id string, at time.Time) error {
	res, err := x.s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, $1) WHERE id = $2 AND subject = $3`,
		storeutil.Ms(at), id, subject)
	return affectedOne(res, err, "revoke token")
}

func (x tokens) RevokeBySubject(ctx context.Context, subject string, at time.Time) error {
	_, err := x.s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = $1 WHERE subject = $2 AND revoked_at IS NULL`,
		storeutil.Ms(at), subject)
	return mapErr("revoke tokens by subject", err)
}

func (x tokens) MarkUsed(ctx context.Context, id string, at time.Time) error {
	res, err := x.s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = $1 WHERE id = $2`, storeutil.Ms(at), id)
	return affectedOne(res, err, "mark token used")
}

// ---- prefs ----

type prefs struct{ s *Store }

func (x prefs) Get(ctx context.Context, subject string) (json.RawMessage, error) {
	var data string
	err := x.s.db.QueryRowContext(ctx, `SELECT data FROM user_prefs WHERE subject = $1`, subject).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, mapErr("get prefs", err)
	}
	return json.RawMessage(data), nil
}

func (x prefs) Put(ctx context.Context, subject string, data json.RawMessage) error {
	if err := storeutil.CheckPrefs(data); err != nil {
		return err
	}
	_, err := x.s.db.ExecContext(ctx,
		`INSERT INTO user_prefs (subject, data, updated_at) VALUES ($1, $2, $3)
		 ON CONFLICT (subject) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		subject, string(data), time.Now().UnixMilli())
	return mapErr("put prefs", err)
}
