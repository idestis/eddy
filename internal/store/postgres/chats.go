package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// ---- chats ----

type chats struct{ s *Store }

const chatCols = `id, owner, title, context, created_at, updated_at, message_count`

const chatMessageCols = `id, chat_id, author, author_type, author_display, via, client, body, meta, created_at`

// chatLockClass is the first key of the per-owner pg_advisory_xact_lock that
// serialises Create, so concurrent creates cannot push an owner past
// store.MaxChatsPerOwner ("chat" in ASCII).
const chatLockClass int32 = 0x63686174

func encodeContext(refs []store.ResourceRef) (string, error) {
	if refs == nil {
		refs = []store.ResourceRef{}
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return "", fmt.Errorf("postgres: encode chat context: %w", err)
	}
	return string(b), nil
}

func decodeContext(s string) ([]store.ResourceRef, error) {
	var refs []store.ResourceRef
	if err := json.Unmarshal([]byte(s), &refs); err != nil {
		return nil, fmt.Errorf("postgres: decode chat context: %w", err)
	}
	return storeutil.ChatContext(refs), nil
}

func scanChat(sc scanner) (store.Chat, error) {
	var (
		c                store.Chat
		ctxJSON          string
		created, updated int64
	)
	if err := sc.Scan(&c.ID, &c.Owner, &c.Title, &ctxJSON, &created, &updated, &c.MessageCount); err != nil {
		return store.Chat{}, err
	}
	refs, err := decodeContext(ctxJSON)
	if err != nil {
		return store.Chat{}, err
	}
	c.Context = refs
	c.CreatedAt, c.UpdatedAt = storeutil.FromMs(created), storeutil.FromMs(updated)
	return c, nil
}

// Create inserts c. Under a per-owner advisory lock it first deletes the
// owner's least recently updated chats beyond MaxChatsPerOwner-1, so the
// owner never holds more than MaxChatsPerOwner chats.
func (x chats) Create(ctx context.Context, c store.Chat) (store.Chat, error) {
	c, err := storeutil.PrepareChat(c)
	if err != nil {
		return store.Chat{}, err
	}
	ctxJSON, err := encodeContext(c.Context)
	if err != nil {
		return store.Chat{}, err
	}
	err = x.s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, chatLockClass, c.Owner); err != nil {
			return mapErr("lock chats", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chats WHERE id IN (
  SELECT id FROM chats WHERE owner = $1 ORDER BY updated_at DESC, id DESC OFFSET $2)`,
			c.Owner, store.MaxChatsPerOwner-1); err != nil {
			return mapErr("evict chats", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO chats (`+chatCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			c.ID, c.Owner, c.Title, ctxJSON, storeutil.Ms(c.CreatedAt), storeutil.Ms(c.UpdatedAt), c.MessageCount)
		return mapErr("insert chat", err)
	})
	if err != nil {
		return store.Chat{}, err
	}
	return c, nil
}

func (x chats) Get(ctx context.Context, owner, id string) (store.Chat, error) {
	c, err := scanChat(x.s.db.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE id = $1 AND owner = $2`, id, owner))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Chat{}, store.ErrNotFound
	}
	if err != nil {
		return store.Chat{}, mapErr("get chat", err)
	}
	return c, nil
}

func (x chats) List(ctx context.Context, owner, cursor string, limit int) ([]store.Chat, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = storeutil.ClampLimit(limit, storeutil.DefaultThreadLimit, storeutil.MaxThreadLimit)

	var a args
	q := `SELECT ` + chatCols + ` FROM chats WHERE owner = ` + a.add(owner)
	if hasCur {
		q += ` AND (updated_at, id) < (` + a.add(cur.Ms) + `, ` + a.add(cur.ID) + `)`
	}
	q += ` ORDER BY updated_at DESC, id DESC LIMIT ` + a.add(limit+1)

	rows, err := x.s.db.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, "", mapErr("list chats", err)
	}
	defer rows.Close()
	var out []store.Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, "", mapErr("list chats", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapErr("list chats", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	last := out[limit-1]
	return out, storeutil.EncodeCursor(storeutil.Ms(last.UpdatedAt), last.ID), nil
}

func (x chats) Update(ctx context.Context, owner, id string, u store.ChatUpdate) (store.Chat, error) {
	var (
		set []string
		a   args
	)
	if u.Title != nil {
		if err := storeutil.CheckChatTitle(*u.Title); err != nil {
			return store.Chat{}, err
		}
		set = append(set, "title = "+a.add(*u.Title))
	}
	if u.Context != nil {
		if err := storeutil.CheckChatContext(*u.Context); err != nil {
			return store.Chat{}, err
		}
		ctxJSON, err := encodeContext(*u.Context)
		if err != nil {
			return store.Chat{}, err
		}
		set = append(set, "context = "+a.add(ctxJSON))
	}
	if len(set) == 0 {
		return x.Get(ctx, owner, id)
	}
	q := `UPDATE chats SET ` + strings.Join(set, ", ") + ` WHERE id = ` + a.add(id) + ` AND owner = ` + a.add(owner) +
		` RETURNING ` + chatCols
	c, err := scanChat(x.s.db.QueryRowContext(ctx, q, a...))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Chat{}, store.ErrNotFound
	}
	if err != nil {
		return store.Chat{}, mapErr("update chat", err)
	}
	return c, nil
}

// AddMessage bumps message_count with a conditional UPDATE before inserting
// the message, like threads.AddMessage: the UPDATE takes the chat's row
// lock, so concurrent messages cannot push it past MaxMessagesPerChat.
func (x chats) AddMessage(ctx context.Context, owner, chatID string, m store.Message) (store.Message, error) {
	m, err := storeutil.PrepareMessage(chatID, m)
	if err != nil {
		return store.Message{}, err
	}
	err = x.s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE chats SET message_count = message_count + 1, updated_at = $1 WHERE id = $2 AND owner = $3 AND message_count < $4`,
			storeutil.Ms(m.CreatedAt), chatID, owner, store.MaxMessagesPerChat)
		if err != nil {
			return mapErr("add chat message", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return mapErr("add chat message", err)
		}
		if n == 0 {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM chats WHERE id = $1 AND owner = $2)`, chatID, owner).Scan(&exists); err != nil {
				return mapErr("add chat message", err)
			}
			if !exists {
				return store.ErrNotFound
			}
			return fmt.Errorf("%w: chat has %d messages", store.ErrConflict, store.MaxMessagesPerChat)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_messages (`+chatMessageCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			m.ID, chatID, m.Author.Subject, string(m.Author.Type), m.Author.Display, m.Author.Via, m.Author.Client,
			m.Body, nullRaw(m.Meta), storeutil.Ms(m.CreatedAt))
		return mapErr("insert chat message", err)
	})
	if err != nil {
		return store.Message{}, err
	}
	return m, nil
}

func (x chats) Messages(ctx context.Context, owner, chatID, cursor string, limit int) ([]store.Message, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = storeutil.ClampLimit(limit, storeutil.DefaultMessageLimit, storeutil.MaxMessageLimit)

	// One snapshot so the ownership check and the page agree.
	tx, err := x.s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, "", mapErr("list chat messages", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM chats WHERE id = $1 AND owner = $2)`, chatID, owner).Scan(&exists); err != nil {
		return nil, "", mapErr("list chat messages", err)
	}
	if !exists {
		return nil, "", store.ErrNotFound
	}

	var a args
	q := `SELECT ` + chatMessageCols + ` FROM chat_messages WHERE chat_id = ` + a.add(chatID)
	if hasCur {
		q += ` AND (created_at, id) > (` + a.add(cur.Ms) + `, ` + a.add(cur.ID) + `)`
	}
	q += ` ORDER BY created_at, id LIMIT ` + a.add(limit+1)

	rows, err := tx.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, "", mapErr("list chat messages", err)
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, "", mapErr("list chat messages", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapErr("list chat messages", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	last := out[limit-1]
	return out, storeutil.EncodeCursor(storeutil.Ms(last.CreatedAt), last.ID), nil
}

func (x chats) Delete(ctx context.Context, owner, id string) error {
	res, err := x.s.db.ExecContext(ctx, `DELETE FROM chats WHERE id = $1 AND owner = $2`, id, owner)
	return affectedOne(res, err, "delete chat")
}
