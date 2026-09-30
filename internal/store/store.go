// Package store persists hub-owned data: sessions, personal access tokens,
// threads and messages, audit events and user preferences. It also holds the
// state hub replicas share with each other: rate-limit counters, the agent
// session registry and cross-replica event notifications. Cluster state is
// never stored here; Kubernetes is the source of truth for it.
//
// The store does no authorization. Callers (internal/hub, internal/mcp)
// filter every read through fleet.Service.
//
// See docs/adr/0004-hub-high-availability.md (and, for the schema reasoning,
// docs/adr/0002-hub-storage.md).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
	ErrLimit    = errors.New("store: limit exceeded")
	// ErrInvalid wraps malformed input such as a bad cursor, enum or JSON.
	ErrInvalid = errors.New("store: invalid input")
)

// Store groups the sub-stores. Consumers depend on the narrow one they need.
type Store interface {
	Sessions() Sessions
	Tokens() Tokens
	Threads() Threads
	Audit() Audit
	Prefs() Prefs
	// RateLimits returns the fixed-window counters shared by every replica.
	RateLimits() RateLimits
	// AgentSessions returns the registry of which replica holds which agent
	// connection.
	AgentSessions() AgentSessions
	// Events returns the cross-replica notification channel.
	Events() Events
	// JoinTokens returns the one-time cluster join tokens (ADR-0005).
	JoinTokens() JoinTokens
	// ConnectionAttempts returns the recent rejected agent connections per
	// cluster, shared by every replica.
	ConnectionAttempts() ConnectionAttempts
	// Prune deletes expired sessions, old tokens, old audit rows, expired
	// threads, ended rate-limit windows, stale agent sessions and join
	// tokens that ended more than JoinTokenPruneAfter ago.
	Prune(ctx context.Context, now time.Time, r Retention) (PruneStats, error)
	Ping(ctx context.Context) error
	Close() error
}

// Session is a browser session. IDHash is sha256 of the cookie value; the
// raw id is never stored.
type Session struct {
	IDHash     []byte
	Subject    string
	Display    string
	Groups     []string
	Provider   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time // min(lastSeen+idle, created+absolute)
	UserAgent  string
	RemoteIP   string
}

type Sessions interface {
	Create(ctx context.Context, s Session) error
	// Get returns ErrNotFound for missing or expired sessions.
	Get(ctx context.Context, idHash []byte, now time.Time) (Session, error)
	// Touch updates LastSeenAt and ExpiresAt. Callers throttle to once a minute.
	Touch(ctx context.Context, idHash []byte, seen, expires time.Time) error
	Delete(ctx context.Context, idHash []byte) error
	DeleteBySubject(ctx context.Context, subject string) error
	// DeleteOldestBySubject keeps the subject's newest keep sessions (by
	// LastSeenAt) and deletes the rest. Enforces auth.session.maxPerUser.
	DeleteOldestBySubject(ctx context.Context, subject string, keep int) error
	// LatestGroups returns the groups of the subject's most recent session,
	// used to shrink proxy users' PAT groups. ErrNotFound if none.
	LatestGroups(ctx context.Context, subject string) ([]string, error)
}

// Token is a personal access token. Hash is HMAC-SHA256(pepper, secret).
type Token struct {
	ID         string // public id, the <id12> part of eddy_pat_<id12><secret32><crc6>
	Hash       []byte
	Subject    string
	Display    string
	Provider   string
	Groups     []string // snapshot at issue time
	Name       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

type Tokens interface {
	// Create inserts t. When maxActive is positive, it first counts the
	// subject's active tokens (not revoked, expiring after t.CreatedAt) and
	// returns ErrLimit if there are maxActive or more. The count and the
	// insert are atomic with respect to other Creates for the same subject,
	// on every replica. A duplicate id or hash returns ErrConflict.
	Create(ctx context.Context, t Token, maxActive int) error
	// Get returns a token by id (including its hash) unless revoked or expired.
	Get(ctx context.Context, id string, now time.Time) (Token, error)
	// List returns the subject's tokens without hashes, newest first.
	List(ctx context.Context, subject string) ([]Token, error)
	CountActive(ctx context.Context, subject string, now time.Time) (int, error)
	Revoke(ctx context.Context, subject, id string, at time.Time) error
	RevokeBySubject(ctx context.Context, subject string, at time.Time) error
	MarkUsed(ctx context.Context, id string, at time.Time) error
}

// ResourceRef is the target of a thread. Kind "" means a cluster-level thread.
type ResourceRef struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type AuthorType string

const (
	AuthorHuman  AuthorType = "human"
	AuthorAI     AuthorType = "ai"
	AuthorSystem AuthorType = "system"
)

// Author records who wrote something. Subject is always the accountable human.
type Author struct {
	Type    AuthorType `json:"type"`
	Subject string     `json:"subject"`
	Display string     `json:"display"`
	Via     string     `json:"via"`              // web | mcp | askai
	Client  string     `json:"client,omitempty"` // e.g. "claude-code", or model id
}

type ThreadType string

const (
	ThreadDiscussion ThreadType = "discussion"
	ThreadAsk        ThreadType = "ask" // Ask AI conversation
)

type Visibility string

const (
	VisibilityResource Visibility = "resource" // anyone who can get the target
	VisibilityPrivate  Visibility = "private"  // only the creator
)

type ThreadStatus string

const (
	ThreadOpen     ThreadStatus = "open"
	ThreadResolved ThreadStatus = "resolved"
)

type Thread struct {
	ID           string       `json:"id"`
	Ref          ResourceRef  `json:"ref"`
	Type         ThreadType   `json:"type"`
	Visibility   Visibility   `json:"visibility"`
	Title        string       `json:"title"`
	Status       ThreadStatus `json:"status"`
	CreatedBy    Author       `json:"createdBy"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
	ResolvedBy   string       `json:"resolvedBy,omitempty"`
	ResolvedAt   *time.Time   `json:"resolvedAt,omitempty"`
	MessageCount int          `json:"messageCount"`
}

type Message struct {
	ID        string          `json:"id"`
	ThreadID  string          `json:"threadId"`
	Author    Author          `json:"author"`
	Body      string          `json:"body"`
	Meta      json.RawMessage `json:"meta,omitempty"` // AI tool steps, usage; never raw objects
	CreatedAt time.Time       `json:"createdAt"`
}

// ThreadFilter narrows List. Empty Ref fields are wildcards. Viewer is used
// only to include the viewer's private threads; resource visibility is the
// caller's job.
type ThreadFilter struct {
	Ref    ResourceRef
	Status ThreadStatus
	Type   ThreadType
	Viewer string
	Cursor string
	Limit  int
}

// Limits enforced by every backend.
const (
	MaxTitleLen          = 200
	MaxMessageBytes      = 64 << 10
	MaxMessagesPerThread = 1000
)

type Threads interface {
	Create(ctx context.Context, t Thread, first Message) (Thread, Message, error)
	Get(ctx context.Context, id string) (Thread, error)
	// List orders by UpdatedAt desc and returns an opaque cursor for the next page.
	List(ctx context.Context, f ThreadFilter) ([]Thread, string, error)
	AddMessage(ctx context.Context, threadID string, m Message) (Message, error)
	Messages(ctx context.Context, threadID, cursor string, limit int) ([]Message, string, error)
	SetStatus(ctx context.Context, id string, st ThreadStatus, by string, at time.Time) error
	Delete(ctx context.Context, id string) error
}

type AuditResult string

const (
	AuditOK     AuditResult = "ok"
	AuditDenied AuditResult = "denied"
	AuditError  AuditResult = "error"
)

type AuditEvent struct {
	ID        int64           `json:"id"`
	Time      time.Time       `json:"ts"`
	RequestID string          `json:"requestId,omitempty"`
	Subject   string          `json:"subject"`
	Groups    []string        `json:"groups"`
	Via       string          `json:"via"`
	TokenID   string          `json:"tokenId,omitempty"`
	Action    string          `json:"action"` // reconcile, suspend, resume, login, token.create, thread.create, mcp.<tool>, ai.ask...
	Target    ResourceRef     `json:"target,omitzero"`
	Result    AuditResult     `json:"result"`
	Detail    json.RawMessage `json:"detail,omitempty"` // ≤ 4 KiB
}

type AuditFilter struct {
	Subject string
	Target  ResourceRef
	Since   time.Time
	Cursor  string
	Limit   int
}

type Audit interface {
	Append(ctx context.Context, e AuditEvent) error
	Query(ctx context.Context, f AuditFilter) ([]AuditEvent, string, error)
}

// Prefs stores per-user UI preferences. Get returns ErrNotFound when none
// are stored. Delete and Revoke methods elsewhere are idempotent.
type Prefs interface {
	Get(ctx context.Context, subject string) (json.RawMessage, error)
	Put(ctx context.Context, subject string, data json.RawMessage) error
}

// Retention configures Prune. A zero value turns that rule off (expired
// sessions are always deleted). The hub passes TokenPurgeAfter itself (30d);
// it is not configurable yet.
type Retention struct {
	AuditDays           int
	ResolvedThreadsDays int // 0 = keep
	AskThreadsDays      int // 0 = keep
	TokenPurgeAfter     time.Duration
}

type PruneStats struct {
	Sessions, Tokens, Audit, Threads int64
	// RateLimits counts ended rate-limit windows; AgentSessions counts agent
	// sessions whose heartbeat is older than AgentSessionPruneAfter.
	RateLimits, AgentSessions int64
	// JoinTokens counts join tokens that expired, were used or were revoked
	// more than JoinTokenPruneAfter ago.
	JoinTokens int64
}

// ---- rate limits ----

// RateLimits is a set of fixed-window counters keyed by an opaque string
// such as "login:user:alice" or "mcp:calls:<token id>". Every replica sees
// the same counters, so a limit holds for the fleet as a whole.
//
// A key's window starts at its first hit and lasts for the window passed
// with that hit; the first hit at or after the window's end starts a new
// window with a count of one. Windows are therefore fixed (not sliding) but
// anchored to the key rather than to the clock, which also lets a single
// hit act as a lockout of exact length (see Get).
//
// Counters are throwaway state: a backend may lose them on a crash, which
// only resets the limit. Concurrency caps ("at most N calls in flight") are
// not modelled here; they stay per replica.
type RateLimits interface {
	// Hit adds one to key's counter and returns the new count and whether
	// it is within limit (count <= limit). A limit of zero or less means no
	// limit. Concurrent hits on one key are counted exactly. The window
	// applies only when Hit starts a new window. window must be positive;
	// key must not be empty (ErrInvalid otherwise).
	Hit(ctx context.Context, key string, window time.Duration, limit int, now time.Time) (count int, allowed bool, err error)
	// Get returns key's count in its current window and when that window
	// ends, without counting a hit. It returns zero values when the key has
	// no window open at now.
	Get(ctx context.Context, key string, now time.Time) (count int, resetAt time.Time, err error)
	// Reset forgets key, for example after a successful login. It is
	// idempotent.
	Reset(ctx context.Context, key string) error
}

// ---- agent sessions ----

// AgentSession records that hub replica HubPod holds a WebSocket from agent
// process AgentInstance for Cluster. Seq is the agent's per-process dial
// counter; a reconnect of the same instance has a higher Seq.
type AgentSession struct {
	Cluster       string
	HubPod        string
	HubAddr       string // host:port of HubPod's peer listener
	AgentInstance string
	Seq           int64
	ConnectedAt   time.Time
	HeartbeatAt   time.Time
}

// AgentSessionPruneAfter is how old a heartbeat must be before Prune deletes
// the session. Readers use a much shorter freshness bound (see List).
const AgentSessionPruneAfter = 5 * time.Minute

// AgentSessions is the registry of agent connections across hub replicas.
// A row is identified by (Cluster, AgentInstance): an agent instance has at
// most one live session, wherever it is. Different instances of one cluster
// coexist (agent replicas).
type AgentSessions interface {
	// Upsert records a session. If the instance already has one, it is
	// replaced when s.Seq is higher, or refreshed when s.Seq is equal and
	// s.HubPod is the same replica; otherwise Upsert returns ErrConflict
	// (a stale dial lost the race). ConnectedAt defaults to now and
	// HeartbeatAt to ConnectedAt. Cluster, HubPod and AgentInstance are
	// required (ErrInvalid).
	Upsert(ctx context.Context, s AgentSession) error
	// Heartbeat sets HeartbeatAt on the session held by hubPod. It returns
	// ErrNotFound when no such session exists, which means another replica
	// has taken the instance over (or the row was pruned) and the caller
	// should drop its connection or Upsert again.
	Heartbeat(ctx context.Context, cluster, hubPod, agentInstance string, at time.Time) error
	// Delete removes the session if hubPod still holds it, so a replica
	// never deletes a session another replica has taken over. Idempotent.
	Delete(ctx context.Context, cluster, hubPod, agentInstance string) error
	// List returns the sessions of cluster ("" means every cluster) whose
	// heartbeat is after freshAfter, oldest connection first (ties broken
	// by HubPod, then AgentInstance). The first entry is the primary.
	List(ctx context.Context, cluster string, freshAfter time.Time) ([]AgentSession, error)
	// DeleteByHub removes every session held by hubPod, for example when a
	// replica starts or drains. Idempotent.
	DeleteByHub(ctx context.Context, hubPod string) error
}

// ---- events ----

// Event kinds. Publishers use EventThread, EventRevoke, EventAgent and
// EventConnection.
// EventResync is sent only by the store, to tell subscribers that events may
// have been missed (for example after the listener reconnected) and that
// they should re-read what they cache.
const (
	EventThread = "thread" // ID is a thread id
	EventRevoke = "revoke" // ID is a subject or token id whose credentials were revoked
	EventAgent  = "agent"  // Cluster's agent sessions changed
	// EventConnection: Cluster's onboarding state changed (a join token was
	// issued or used, or an agent connection was rejected).
	EventConnection = "connection"
	EventResync     = "resync"
)

// MaxEventBytes caps the JSON encoding of one Event.
const MaxEventBytes = 1 << 10

// Event is a cross-replica notification. It carries ids only; receivers
// re-read the rows they care about.
type Event struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Cluster string `json:"cluster,omitempty"`
}

// Events fans notifications out to every replica, including the publisher.
// Delivery is best effort: an event can be lost when a subscriber falls
// behind or the connection to the database drops, so subscribers must also
// re-read state periodically. After a reconnect the store sends an
// EventResync.
type Events interface {
	// Publish sends e to every subscriber on every replica. e.Kind must be
	// EventThread, EventRevoke, EventAgent or EventConnection (ErrInvalid
	// otherwise), and its
	// JSON encoding at most MaxEventBytes (ErrLimit otherwise).
	Publish(ctx context.Context, e Event) error
	// Subscribe returns a channel of events. Events published after
	// Subscribe returns are delivered. The channel is closed when ctx is
	// done or the store is closed.
	Subscribe(ctx context.Context) (<-chan Event, error)
}

// ---- join tokens ----

// JoinToken is a one-time token that lets an agent claim Cluster once
// (ADR-0005). Hash is HMAC-SHA256(pepper, secret); the raw token is shown
// once and never stored. A token is usable while UsedAt and RevokedAt are
// nil and ExpiresAt is after now.
type JoinToken struct {
	ID        string // public id, the <id12> part of eddy_join_<id12><secret32><crc6>
	Cluster   string
	Hash      []byte
	CreatedBy string // subject of the user who issued it
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	RevokedAt *time.Time
}

// JoinTokenPruneAfter is how long Prune keeps a join token after it
// expired, was used or was revoked, for the audit trail.
const JoinTokenPruneAfter = 30 * 24 * time.Hour

// JoinTokens stores join tokens. Rows are durable (a LOGGED table) so the
// issue and use of every token can be traced.
type JoinTokens interface {
	// Create inserts t and, atomically with it, revokes (RevokedAt =
	// t.CreatedAt) every other token of t.Cluster that is neither used
	// nor revoked yet, so a cluster has at most one live join token. A
	// duplicate id or hash returns ErrConflict. ID, Cluster, Hash and
	// ExpiresAt are required (ErrInvalid).
	Create(ctx context.Context, t JoinToken) error
	// Get returns a token by id, with its hash, whatever its state.
	// ErrNotFound if there is none.
	Get(ctx context.Context, id string) (JoinToken, error)
	// Consume marks the token used at now, only if it is still unused,
	// unrevoked and unexpired, and returns it. Exactly one of several
	// concurrent Consume calls for a token succeeds, on every replica; the
	// others get ErrNotFound (as do calls for an unusable or unknown id).
	Consume(ctx context.Context, id string, now time.Time) (JoinToken, error)
	// List returns the tokens of cluster without hashes, newest first.
	List(ctx context.Context, cluster string) ([]JoinToken, error)
	// RevokeByCluster revokes every live token of cluster, for example when
	// the cluster is deleted. Idempotent.
	RevokeByCluster(ctx context.Context, cluster string, at time.Time) error
}

// ---- connection attempts ----

// Reasons of rejected agent connections.
const (
	AttemptBadToken        = "bad_token"         // unknown, rotated or mistyped agent token
	AttemptJoinExpired     = "join_expired"      // the join token expired or was replaced
	AttemptJoinUsed        = "join_used"         // the join token was already used
	AttemptWrongCluster    = "wrong_cluster"     // the token belongs to another cluster
	AttemptProtocol        = "protocol_mismatch" // the agent speaks another protocol version
	AttemptHelloRejected   = "hello_rejected"    // any other invalid hello
	AttemptCredentialsFail = "credentials_failed"
)

// MaxAttemptsPerCluster is how many rejected attempts are kept per cluster.
const MaxAttemptsPerCluster = 20

// ConnectionAttempt is one rejected agent connection. Detail and Peer are
// short free text; neither ever contains a token.
type ConnectionAttempt struct {
	Cluster string    `json:"-"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
	Detail  string    `json:"detail,omitempty"`
	Peer    string    `json:"peer,omitempty"`
	HubPod  string    `json:"hubPod,omitempty"`
}

// ConnectionAttempts keeps the last MaxAttemptsPerCluster rejected
// connections of every cluster. It is throwaway state (an UNLOGGED table):
// callers record attempts only for registered clusters, so unknown names
// cannot fill it.
type ConnectionAttempts interface {
	// Record stores a and deletes the cluster's attempts beyond the newest
	// MaxAttemptsPerCluster. Cluster and Reason are required (ErrInvalid).
	Record(ctx context.Context, a ConnectionAttempt) error
	// List returns the cluster's attempts, newest first.
	List(ctx context.Context, cluster string) ([]ConnectionAttempt, error)
	// DeleteByCluster forgets a cluster's attempts. Idempotent.
	DeleteByCluster(ctx context.Context, cluster string) error
}
