// Package store persists hub-owned data: sessions, personal access tokens,
// threads and messages, audit events and user preferences. Cluster state is
// never stored here; Kubernetes is the source of truth for it.
//
// The store does no authorization. Callers (internal/hub, internal/mcp)
// filter every read through fleet.Service.
//
// See docs/adr/0002-hub-storage.md.
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
)

// Store groups the sub-stores. Consumers depend on the narrow one they need.
type Store interface {
	Sessions() Sessions
	Tokens() Tokens
	Threads() Threads
	Audit() Audit
	Prefs() Prefs
	// Prune deletes expired sessions, old tokens, old audit rows and expired threads.
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
	Create(ctx context.Context, t Token) error
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

type Prefs interface {
	Get(ctx context.Context, subject string) (json.RawMessage, error)
	Put(ctx context.Context, subject string, data json.RawMessage) error
}

// Retention configures Prune.
type Retention struct {
	AuditDays           int
	ResolvedThreadsDays int // 0 = keep
	AskThreadsDays      int // 0 = keep
	TokenPurgeAfter     time.Duration
}

type PruneStats struct {
	Sessions, Tokens, Audit, Threads int64
}
