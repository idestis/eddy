package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/eddy-gitops/eddy/internal/store"
	"github.com/eddy-gitops/eddy/internal/store/internal/storeutil"
)

func encodeStrings(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("sqlite: encode list: %w", err)
	}
	return string(b), nil
}

func decodeStrings(s string) ([]string, error) {
	var v []string
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("sqlite: decode list: %w", err)
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
	_, err = x.s.w.ExecContext(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.IDHash, v.Subject, v.Display, groups, v.Provider,
		storeutil.Ms(v.CreatedAt), storeutil.Ms(v.LastSeenAt), storeutil.Ms(v.ExpiresAt), v.UserAgent, v.RemoteIP)
	if isConstraint(err) {
		return store.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("sqlite: create session: %w", err)
	}
	return nil
}

func (x sessions) Get(ctx context.Context, idHash []byte, now time.Time) (store.Session, error) {
	row := x.s.r.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id_hash = ? AND expires_at > ?`,
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
		return store.Session{}, fmt.Errorf("sqlite: get session: %w", err)
	}
	if v.Groups, err = decodeStrings(groups); err != nil {
		return store.Session{}, err
	}
	v.CreatedAt, v.LastSeenAt, v.ExpiresAt = storeutil.FromMs(created), storeutil.FromMs(seen), storeutil.FromMs(expiresMs)
	return v, nil
}

func (x sessions) Touch(ctx context.Context, idHash []byte, seen, expires time.Time) error {
	res, err := x.s.w.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ?`,
		storeutil.Ms(seen), storeutil.Ms(expires), idHash)
	return affectedOne(res, err, "touch session")
}

func (x sessions) Delete(ctx context.Context, idHash []byte) error {
	if _, err := x.s.w.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash); err != nil {
		return fmt.Errorf("sqlite: delete session: %w", err)
	}
	return nil
}

func (x sessions) DeleteBySubject(ctx context.Context, subject string) error {
	if _, err := x.s.w.ExecContext(ctx, `DELETE FROM sessions WHERE subject = ?`, subject); err != nil {
		return fmt.Errorf("sqlite: delete sessions by subject: %w", err)
	}
	return nil
}

func (x sessions) LatestGroups(ctx context.Context, subject string) ([]string, error) {
	var groups string
	err := x.s.r.QueryRowContext(ctx,
		`SELECT groups FROM sessions WHERE subject = ? ORDER BY last_seen_at DESC, created_at DESC, id_hash DESC LIMIT 1`,
		subject).Scan(&groups)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: latest session groups: %w", err)
	}
	return decodeStrings(groups)
}

// affectedOne turns "no row updated" into store.ErrNotFound.
func affectedOne(res sql.Result, err error, op string) error {
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", op, err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ---- tokens ----

type tokens struct{ s *Store }

const tokenCols = `id, hash, subject, display, provider, groups, name, scopes, created_at, expires_at, last_used_at, revoked_at`

func (x tokens) Create(ctx context.Context, t store.Token) error {
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
	_, err = x.s.w.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Hash, t.Subject, t.Display, t.Provider, groups, t.Name, scopes,
		storeutil.Ms(t.CreatedAt), storeutil.Ms(t.ExpiresAt), nullMs(t.LastUsedAt), nullMs(t.RevokedAt))
	if isConstraint(err) {
		return store.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("sqlite: create token: %w", err)
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

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
	t, err := scanToken(x.s.r.QueryRowContext(ctx,
		`SELECT `+tokenCols+` FROM api_tokens WHERE id = ? AND revoked_at IS NULL AND expires_at > ?`,
		id, storeutil.Ms(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Token{}, store.ErrNotFound
	}
	if err != nil {
		return store.Token{}, fmt.Errorf("sqlite: get token: %w", err)
	}
	return t, nil
}

func (x tokens) List(ctx context.Context, subject string) ([]store.Token, error) {
	rows, err := x.s.r.QueryContext(ctx,
		`SELECT `+tokenCols+` FROM api_tokens WHERE subject = ? ORDER BY created_at DESC, id DESC`, subject)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list tokens: %w", err)
	}
	defer rows.Close()
	var out []store.Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list tokens: %w", err)
		}
		t.Hash = nil // List never returns hashes.
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list tokens: %w", err)
	}
	return out, nil
}

func (x tokens) CountActive(ctx context.Context, subject string, now time.Time) (int, error) {
	var n int
	if err := x.s.r.QueryRowContext(ctx,
		`SELECT count(*) FROM api_tokens WHERE subject = ? AND revoked_at IS NULL AND expires_at > ?`,
		subject, storeutil.Ms(now)).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite: count tokens: %w", err)
	}
	return n, nil
}

func (x tokens) Revoke(ctx context.Context, subject, id string, at time.Time) error {
	res, err := x.s.w.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ? AND subject = ?`,
		storeutil.Ms(at), id, subject)
	return affectedOne(res, err, "revoke token")
}

func (x tokens) RevokeBySubject(ctx context.Context, subject string, at time.Time) error {
	if _, err := x.s.w.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ? WHERE subject = ? AND revoked_at IS NULL`,
		storeutil.Ms(at), subject); err != nil {
		return fmt.Errorf("sqlite: revoke tokens by subject: %w", err)
	}
	return nil
}

func (x tokens) MarkUsed(ctx context.Context, id string, at time.Time) error {
	res, err := x.s.w.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, storeutil.Ms(at), id)
	return affectedOne(res, err, "mark token used")
}

// ---- prefs ----

type prefs struct{ s *Store }

func (x prefs) Get(ctx context.Context, subject string) (json.RawMessage, error) {
	var data string
	err := x.s.r.QueryRowContext(ctx, `SELECT data FROM user_prefs WHERE subject = ?`, subject).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get prefs: %w", err)
	}
	return json.RawMessage(data), nil
}

func (x prefs) Put(ctx context.Context, subject string, data json.RawMessage) error {
	if err := storeutil.CheckPrefs(data); err != nil {
		return err
	}
	if _, err := x.s.w.ExecContext(ctx,
		`INSERT INTO user_prefs (subject, data, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (subject) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		subject, string(data), time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("sqlite: put prefs: %w", err)
	}
	return nil
}
