-- 0003_chats: Ask AI chats that belong to their owner, not to a resource
-- (docs/adr/0007-ask-ai-chats.md). They replace threads of type 'ask'.
--
-- v1.0 is unreleased, so 'ask' threads are dropped rather than migrated.
-- The threads.type column stays (review threads are 'discussion'); it no
-- longer accepts 'ask'.

DELETE FROM threads WHERE type = 'ask';
DROP INDEX IF EXISTS threads_ask;
ALTER TABLE threads DROP CONSTRAINT threads_type_valid;
ALTER TABLE threads ADD CONSTRAINT threads_type_valid CHECK (type IN ('discussion'));

-- LOGGED on purpose: an UNLOGGED table is truncated after a crash and not
-- replicated to standbys, so a failover would erase every chat. The janitor
-- deletes chats idle for longer than store.retention.chatDays instead.
CREATE TABLE chats (
  id            TEXT COLLATE "C" PRIMARY KEY, -- UUIDv7
  owner         TEXT COLLATE "C" NOT NULL,    -- the accountable human's subject
  title         TEXT NOT NULL CONSTRAINT chats_title_len CHECK (char_length(title) <= 200),
  context       TEXT NOT NULL DEFAULT '[]'    -- JSON array of ResourceRef, at most 10
                CONSTRAINT chats_context_len CHECK (octet_length(context) <= 16384),
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  message_count INTEGER NOT NULL DEFAULT 0 CONSTRAINT chats_message_count_max CHECK (message_count BETWEEN 0 AND 1000)
);
CREATE INDEX chats_owner_recent ON chats (owner, updated_at DESC, id DESC);
CREATE INDEX chats_updated ON chats (updated_at);

CREATE TABLE chat_messages (
  id             TEXT COLLATE "C" PRIMARY KEY, -- UUIDv7
  chat_id        TEXT COLLATE "C" NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
  author         TEXT NOT NULL,                -- the accountable human's subject
  author_type    TEXT NOT NULL CONSTRAINT chat_messages_author_type_valid CHECK (author_type IN ('human', 'ai', 'system')),
  author_display TEXT NOT NULL DEFAULT '',
  via            TEXT NOT NULL,                -- web | askai
  client         TEXT NOT NULL DEFAULT '',     -- model id for AI answers
  body           TEXT NOT NULL CONSTRAINT chat_messages_body_len CHECK (octet_length(body) <= 65536),
  meta           TEXT,                         -- JSON: AI tool steps, usage; never raw objects
  created_at     BIGINT NOT NULL
);
CREATE INDEX chat_messages_chat ON chat_messages (chat_id, created_at, id);
