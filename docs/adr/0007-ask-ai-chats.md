# ADR-0007: Ask AI chats that follow the user, not the resource

- **Status:** Accepted · **Date:** 2026-10-01 · **Amends:** ADR-0003 §6 (Ask AI)
- **Context:** In v1.0 drafts every Ask AI conversation was a private thread of type `ask` tied to one
  resource. Moving to another resource left the conversation behind, which made Ask AI hard to use
  for the real job: following a problem across a HelmRelease, its Deployment, a PVC and a node.

## Decision

1. **A chat belongs to its owner, not to a resource.** It has an owner (the accountable human), a
   title, an ordered list of **context** references and its messages. Moving around the UI keeps
   the open chat; it never switches by itself.
2. **Context is explicit and editable.** A chat carries up to 10 references (`ResourceRef`, and
   `kind: ""` for a whole cluster), from any cluster. A new chat starts with what is on screen: the
   selected resource, else the current cluster, else nothing. The user adds more with a **+** picker
   or an **@mention** in the question, and removes them with ×. Chips read like the rest of the UI:
   kind badge, `namespace / name`, cluster when it differs from the current one.
3. **History is per user**: a list of the user's chats, newest first, with titles generated from the
   first question. Nobody else can list or open them, including admins through the API.
4. **Retention:** chats are kept for `store.retention.chatDays` (default 30) after their last
   message, then the janitor deletes them. A user keeps at most 200 chats; creating one more drops
   the oldest.
5. **Storage:** ordinary (LOGGED) PostgreSQL tables `chats` and `chat_messages`, not UNLOGGED.
   UNLOGGED tables are truncated after a crash and are not replicated to standbys, so a
   CloudNativePG failover would erase every chat; a TTL gives the same cost control.
6. **`ask` threads go away.** v1.0 is unreleased, so there is nothing to migrate: thread type `ask`,
   `retention.askThreadsDays` and `POST /api/v1/ai/ask {threadId}` are replaced. Review threads
   (`discussion`) are unchanged, and **Save as thread** still copies an answer into one.

## Security

- **Owner only.** Every chat endpoint checks `chat.owner == principal.User`; anything else is 404.
- **Context is re-checked on every ask.** Each reference is checked with the SAR authorizer as the
  asking user (`get` on the kind, or the cluster-level check for `kind: ""`). References the user can
  no longer see are not sent to the model and come back as `contextStatus[i] = "hidden"`; the user
  added them, so their names are not a leak. Tool calls are unchanged: impersonated or SAR-filtered.
- **Context is data, not instructions.** The summaries of the context resources are fetched with the
  same redaction and nonce-wrapped untrusted-data blocks as tool results. The question stays the
  only instruction.
- **History is data too.** Earlier messages of the chat (the last 10, each capped at 2 KiB) reach the
  model as one redacted untrusted-data block, not as earlier conversation turns, so text in an
  earlier answer cannot act as an instruction.
- **Limits:** 10 context references, a question of at most 8 KiB, 1000 messages per chat, message
  bodies at most 64 KiB, and the existing per-user Ask AI rate limit. Attachments (`logs`, `yaml`)
  keep their limits and are still never stored.
- **Kill switch:** `aiEnabled: false` returns 503 on asks; listing, reading and deleting chats keep
  working, so users can still read and remove their history.
- **Audit:** `ai.ask` records the chat id and the context references, never the answer text unless
  `ai.auditPrompts` is set.
- **@mention search** uses `GET /api/v1/search`, which is already SAR-filtered.

## API

See [`docs/api.md`](../api.md#ask-ai). In short:

| Method and path | Purpose |
|---|---|
| `GET /api/v1/ai/chats?cursor=&limit=` | the caller's chats, newest first |
| `POST /api/v1/ai/chats` `{context?}` | create an empty chat |
| `GET /api/v1/ai/chats/{id}?cursor=` | the chat and its messages |
| `PATCH /api/v1/ai/chats/{id}` `{title?, context?}` | rename, or replace the context |
| `DELETE /api/v1/ai/chats/{id}` | delete it |
| `POST /api/v1/ai/ask` `{chatId?, context?, question, attachments?}` | ask; without `chatId` a chat is created with `context` |

## Consequences

- The UI keeps the open chat id in the app (not in a route), so it survives navigation and reloads
  (per tab, `sessionStorage`), and the chat list is one request away.
- MCP is unaffected: it has no Ask AI tools.
- The store gains a `Chats` interface with its own conformance tests in `storetest`.
