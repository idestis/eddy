package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// eq is an optional "column = value" filter; empty values are wildcards.
type eq struct{ col, val string }

// ---- threads ----

type threads struct{ s *Store }

const threadCols = `id, cluster, api_group, kind, namespace, name, type, visibility, status, title,
created_by, created_by_type, created_by_display, created_via, created_client,
created_at, updated_at, resolved_by, resolved_at, message_count`

const messageCols = `id, thread_id, author, author_type, author_display, via, client, body, meta, created_at`

func scanThread(sc scanner) (store.Thread, error) {
	var (
		t                store.Thread
		created, updated int64
		resolvedBy       sql.NullString
		resolvedAt       sql.NullInt64
	)
	if err := sc.Scan(&t.ID, &t.Ref.Cluster, &t.Ref.Group, &t.Ref.Kind, &t.Ref.Namespace, &t.Ref.Name,
		&t.Type, &t.Visibility, &t.Status, &t.Title,
		&t.CreatedBy.Subject, &t.CreatedBy.Type, &t.CreatedBy.Display, &t.CreatedBy.Via, &t.CreatedBy.Client,
		&created, &updated, &resolvedBy, &resolvedAt, &t.MessageCount); err != nil {
		return store.Thread{}, err
	}
	t.CreatedAt, t.UpdatedAt = storeutil.FromMs(created), storeutil.FromMs(updated)
	t.ResolvedBy = resolvedBy.String
	t.ResolvedAt = fromNullMs(resolvedAt)
	return t, nil
}

func scanMessage(sc scanner) (store.Message, error) {
	var (
		m       store.Message
		meta    sql.NullString
		created int64
	)
	if err := sc.Scan(&m.ID, &m.ThreadID, &m.Author.Subject, &m.Author.Type, &m.Author.Display,
		&m.Author.Via, &m.Author.Client, &m.Body, &meta, &created); err != nil {
		return store.Message{}, err
	}
	if meta.Valid && meta.String != "" {
		m.Meta = json.RawMessage(meta.String)
	}
	m.CreatedAt = storeutil.FromMs(created)
	return m, nil
}

func nullRaw(r json.RawMessage) sql.NullString {
	if len(r) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(r), Valid: true}
}

func insertMessage(ctx context.Context, tx *sql.Tx, m store.Message) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO messages (`+messageCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		m.ID, m.ThreadID, m.Author.Subject, string(m.Author.Type), m.Author.Display, m.Author.Via, m.Author.Client,
		m.Body, nullRaw(m.Meta), storeutil.Ms(m.CreatedAt))
	return mapErr("insert message", err)
}

func (x threads) Create(ctx context.Context, t store.Thread, first store.Message) (store.Thread, store.Message, error) {
	t, first, err := storeutil.PrepareThread(t, first)
	if err != nil {
		return store.Thread{}, store.Message{}, err
	}
	err = x.s.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO threads (`+threadCols+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, NULL, NULL, $18)`,
			t.ID, t.Ref.Cluster, t.Ref.Group, t.Ref.Kind, t.Ref.Namespace, t.Ref.Name,
			string(t.Type), string(t.Visibility), string(t.Status), t.Title,
			t.CreatedBy.Subject, string(t.CreatedBy.Type), t.CreatedBy.Display, t.CreatedBy.Via, t.CreatedBy.Client,
			storeutil.Ms(t.CreatedAt), storeutil.Ms(t.UpdatedAt), t.MessageCount)
		if err != nil {
			return mapErr("insert thread", err)
		}
		return insertMessage(ctx, tx, first)
	})
	if err != nil {
		return store.Thread{}, store.Message{}, err
	}
	return t, first, nil
}

func (x threads) Get(ctx context.Context, id string) (store.Thread, error) {
	t, err := scanThread(x.s.db.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Thread{}, store.ErrNotFound
	}
	if err != nil {
		return store.Thread{}, mapErr("get thread", err)
	}
	return t, nil
}

func (x threads) List(ctx context.Context, f store.ThreadFilter) ([]store.Thread, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := storeutil.ClampLimit(f.Limit, storeutil.DefaultThreadLimit, storeutil.MaxThreadLimit)

	var (
		where []string
		a     args
	)
	for _, c := range []eq{
		{"cluster", f.Ref.Cluster}, {"api_group", f.Ref.Group}, {"kind", f.Ref.Kind},
		{"namespace", f.Ref.Namespace}, {"name", f.Ref.Name},
		{"status", string(f.Status)}, {"type", string(f.Type)},
	} {
		if c.val != "" {
			where = append(where, c.col+" = "+a.add(c.val))
		}
	}
	viewer := a.add(f.Viewer)
	where = append(where, "(visibility = 'resource' OR ("+viewer+" <> '' AND created_by = "+viewer+"))")
	if hasCur {
		where = append(where, "(updated_at, id) < ("+a.add(cur.Ms)+", "+a.add(cur.ID)+")")
	}
	q := `SELECT ` + threadCols + ` FROM threads WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY updated_at DESC, id DESC LIMIT ` + a.add(limit+1)

	rows, err := x.s.db.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, "", mapErr("list threads", err)
	}
	defer rows.Close()
	var out []store.Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, "", mapErr("list threads", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapErr("list threads", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	last := out[limit-1]
	return out, storeutil.EncodeCursor(storeutil.Ms(last.UpdatedAt), last.ID), nil
}

// AddMessage bumps message_count with a conditional UPDATE before inserting
// the message. The UPDATE takes the thread's row lock, so concurrent
// replies queue on it and re-check the limit against the committed count:
// no reply can push the thread past store.MaxMessagesPerThread.
func (x threads) AddMessage(ctx context.Context, threadID string, m store.Message) (store.Message, error) {
	m, err := storeutil.PrepareMessage(threadID, m)
	if err != nil {
		return store.Message{}, err
	}
	err = x.s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE threads SET message_count = message_count + 1, updated_at = $1 WHERE id = $2 AND message_count < $3`,
			storeutil.Ms(m.CreatedAt), threadID, store.MaxMessagesPerThread)
		if err != nil {
			return mapErr("add message", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return mapErr("add message", err)
		}
		if n == 0 {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM threads WHERE id = $1)`, threadID).Scan(&exists); err != nil {
				return mapErr("add message", err)
			}
			if !exists {
				return store.ErrNotFound
			}
			return fmt.Errorf("%w: thread has %d messages", store.ErrLimit, store.MaxMessagesPerThread)
		}
		return insertMessage(ctx, tx, m)
	})
	if err != nil {
		return store.Message{}, err
	}
	return m, nil
}

func (x threads) Messages(ctx context.Context, threadID, cursor string, limit int) ([]store.Message, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = storeutil.ClampLimit(limit, storeutil.DefaultMessageLimit, storeutil.MaxMessageLimit)

	// One snapshot so the existence check and the page agree.
	tx, err := x.s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, "", mapErr("list messages", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM threads WHERE id = $1)`, threadID).Scan(&exists); err != nil {
		return nil, "", mapErr("list messages", err)
	}
	if !exists {
		return nil, "", store.ErrNotFound
	}

	var a args
	q := `SELECT ` + messageCols + ` FROM messages WHERE thread_id = ` + a.add(threadID)
	if hasCur {
		q += ` AND (created_at, id) > (` + a.add(cur.Ms) + `, ` + a.add(cur.ID) + `)`
	}
	q += ` ORDER BY created_at, id LIMIT ` + a.add(limit+1)

	rows, err := tx.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, "", mapErr("list messages", err)
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, "", mapErr("list messages", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapErr("list messages", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	last := out[limit-1]
	return out, storeutil.EncodeCursor(storeutil.Ms(last.CreatedAt), last.ID), nil
}

func (x threads) SetStatus(ctx context.Context, id string, st store.ThreadStatus, by string, at time.Time) error {
	if err := storeutil.CheckStatus(st); err != nil {
		return err
	}
	at = storeutil.NowIfZero(at)
	var (
		res sql.Result
		err error
	)
	if st == store.ThreadResolved {
		res, err = x.s.db.ExecContext(ctx,
			`UPDATE threads SET status = 'resolved', resolved_by = $1, resolved_at = $2, updated_at = $2 WHERE id = $3`,
			by, storeutil.Ms(at), id)
	} else {
		res, err = x.s.db.ExecContext(ctx,
			`UPDATE threads SET status = 'open', resolved_by = NULL, resolved_at = NULL, updated_at = $1 WHERE id = $2`,
			storeutil.Ms(at), id)
	}
	return affectedOne(res, err, "set thread status")
}

func (x threads) Delete(ctx context.Context, id string) error {
	res, err := x.s.db.ExecContext(ctx, `DELETE FROM threads WHERE id = $1`, id)
	return affectedOne(res, err, "delete thread")
}

// ---- audit ----

type audit struct{ s *Store }

const auditCols = `id, ts, request_id, subject, groups, via, token_id, action, cluster, api_group, kind, namespace, name, result, detail`

func (x audit) Append(ctx context.Context, e store.AuditEvent) error {
	e, err := storeutil.PrepareAudit(e)
	if err != nil {
		return err
	}
	groups, err := encodeStrings(e.Groups)
	if err != nil {
		return err
	}
	_, err = x.s.db.ExecContext(ctx, `INSERT INTO audit_events (`+auditCols[len("id, "):]+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		storeutil.Ms(e.Time), e.RequestID, e.Subject, groups, e.Via, e.TokenID, e.Action,
		e.Target.Cluster, e.Target.Group, e.Target.Kind, e.Target.Namespace, e.Target.Name,
		string(e.Result), nullRaw(e.Detail))
	return mapErr("append audit", err)
}

func (x audit) Query(ctx context.Context, f store.AuditFilter) ([]store.AuditEvent, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	var curID int64
	if hasCur {
		if curID, err = strconv.ParseInt(cur.ID, 10, 64); err != nil {
			return nil, "", fmt.Errorf("%w: %v", storeutil.ErrInvalidCursor, err)
		}
	}
	limit := storeutil.ClampLimit(f.Limit, storeutil.DefaultAuditLimit, storeutil.MaxAuditLimit)

	var (
		where = []string{"TRUE"}
		a     args
	)
	for _, c := range []eq{
		{"subject", f.Subject}, {"cluster", f.Target.Cluster}, {"api_group", f.Target.Group},
		{"kind", f.Target.Kind}, {"namespace", f.Target.Namespace}, {"name", f.Target.Name},
	} {
		if c.val != "" {
			where = append(where, c.col+" = "+a.add(c.val))
		}
	}
	if !f.Since.IsZero() {
		where = append(where, "ts >= "+a.add(storeutil.Ms(f.Since)))
	}
	if hasCur {
		where = append(where, "(ts, id) < ("+a.add(cur.Ms)+", "+a.add(curID)+")")
	}
	q := `SELECT ` + auditCols + ` FROM audit_events WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ts DESC, id DESC LIMIT ` + a.add(limit+1)

	rows, err := x.s.db.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, "", mapErr("query audit", err)
	}
	defer rows.Close()
	var out []store.AuditEvent
	for rows.Next() {
		var (
			e      store.AuditEvent
			ts     int64
			groups string
			result string
			detail sql.NullString
		)
		if err := rows.Scan(&e.ID, &ts, &e.RequestID, &e.Subject, &groups, &e.Via, &e.TokenID, &e.Action,
			&e.Target.Cluster, &e.Target.Group, &e.Target.Kind, &e.Target.Namespace, &e.Target.Name,
			&result, &detail); err != nil {
			return nil, "", mapErr("query audit", err)
		}
		e.Time = storeutil.FromMs(ts)
		e.Result = store.AuditResult(result)
		if detail.Valid && detail.String != "" {
			e.Detail = json.RawMessage(detail.String)
		}
		if e.Groups, err = decodeStrings(groups); err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapErr("query audit", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	last := out[limit-1]
	return out, storeutil.EncodeCursor(storeutil.Ms(last.Time), strconv.FormatInt(last.ID, 10)), nil
}
