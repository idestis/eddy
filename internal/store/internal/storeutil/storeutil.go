// Package storeutil holds the pieces every store backend must share so that
// they behave identically: cursor encoding, page-size limits, input
// validation and time normalisation. It is internal to internal/store.
package storeutil

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/idestis/eddy/internal/store"
)

// Page sizes. A limit of zero or less selects the default; larger limits are
// clamped to the maximum.
const (
	DefaultThreadLimit  = 50
	MaxThreadLimit      = 200
	DefaultMessageLimit = 100
	MaxMessageLimit     = 500
	DefaultAuditLimit   = 100
	MaxAuditLimit       = 500
)

// Other limits enforced by every backend.
const (
	// MaxPrefsBytes caps one user's preferences document.
	MaxPrefsBytes = 16 << 10
	// MaxAuditDetailBytes caps AuditEvent.Detail.
	MaxAuditDetailBytes = 4 << 10
	// PruneBatch is the number of rows deleted per statement by Prune, so the
	// janitor never holds the write lock for long.
	PruneBatch = 5000
)

// ErrInvalidCursor is returned (wrapped) for cursors that were not produced
// by the store.
var ErrInvalidCursor = fmt.Errorf("%w: cursor", store.ErrInvalid)

// ClampLimit applies the default and maximum page size.
func ClampLimit(limit, def, maxLimit int) int {
	if limit <= 0 {
		return def
	}
	return min(limit, maxLimit)
}

// Cursor is a keyset position: the sort timestamp (unix ms) and the tie-break id
// of the last row on the previous page.
type Cursor struct {
	Ms int64
	ID string
}

// EncodeCursor returns the opaque form of c: base64url("<ms>:<id>").
func EncodeCursor(ms int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(ms, 10) + ":" + id))
}

// DecodeCursor parses an opaque cursor. The empty string yields ok == false
// and no error.
func DecodeCursor(s string) (c Cursor, ok bool, err error) {
	if s == "" {
		return Cursor{}, false, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, false, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	msPart, id, found := strings.Cut(string(b), ":")
	if !found || id == "" {
		return Cursor{}, false, ErrInvalidCursor
	}
	ms, err := strconv.ParseInt(msPart, 10, 64)
	if err != nil {
		return Cursor{}, false, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return Cursor{Ms: ms, ID: id}, true, nil
}

// Ms converts t to unix milliseconds, the storage resolution of every backend.
func Ms(t time.Time) int64 { return t.UnixMilli() }

// FromMs converts unix milliseconds back to a UTC time.
func FromMs(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// Norm truncates t to the storage resolution and converts it to UTC, so a
// value read back from any backend equals the value written.
func Norm(t time.Time) time.Time { return FromMs(Ms(t)) }

// NormPtr is Norm for optional times.
func NormPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	n := Norm(*t)
	return &n
}

// NowIfZero returns t, or the current time when t is zero, normalised.
func NowIfZero(t time.Time) time.Time {
	if t.IsZero() {
		t = time.Now()
	}
	return Norm(t)
}

// NewID returns a UUIDv7 string. UUIDv7 ids sort by creation time.
func NewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("store: generate id: %w", err)
	}
	return id.String(), nil
}

// Strings returns a copy of s, or nil when s is empty, so every backend
// returns the same value for "no entries".
func Strings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// Raw returns a copy of r, or nil when r is empty.
func Raw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(r))
	copy(out, r)
	return out
}

// Bytes returns a copy of b, or nil when b is empty.
func Bytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// PrepareThread validates and normalises a thread and its first message for
// Threads.Create, generating ids and timestamps where missing.
func PrepareThread(t store.Thread, first store.Message) (store.Thread, store.Message, error) {
	if t.Ref.Cluster == "" {
		return t, first, errors.New("store: thread ref.cluster is required")
	}
	switch t.Type {
	case store.ThreadDiscussion, store.ThreadAsk:
	default:
		return t, first, fmt.Errorf("store: invalid thread type %q", t.Type)
	}
	switch t.Visibility {
	case store.VisibilityResource, store.VisibilityPrivate:
	default:
		return t, first, fmt.Errorf("store: invalid thread visibility %q", t.Visibility)
	}
	if utf8.RuneCountInString(t.Title) > store.MaxTitleLen {
		return t, first, fmt.Errorf("%w: title longer than %d characters", store.ErrLimit, store.MaxTitleLen)
	}
	if err := checkAuthor(t.CreatedBy); err != nil {
		return t, first, err
	}
	if t.ID == "" {
		id, err := NewID()
		if err != nil {
			return t, first, err
		}
		t.ID = id
	}
	t.CreatedAt = NowIfZero(t.CreatedAt)
	t.UpdatedAt = t.CreatedAt
	t.Status = store.ThreadOpen
	t.ResolvedBy = ""
	t.ResolvedAt = nil
	t.MessageCount = 1

	if first.CreatedAt.IsZero() {
		first.CreatedAt = t.CreatedAt
	}
	first, err := PrepareMessage(t.ID, first)
	if err != nil {
		return t, first, err
	}
	return t, first, nil
}

// PrepareMessage validates and normalises a message for AddMessage.
func PrepareMessage(threadID string, m store.Message) (store.Message, error) {
	if len(m.Body) > store.MaxMessageBytes {
		return m, fmt.Errorf("%w: message body larger than %d bytes", store.ErrLimit, store.MaxMessageBytes)
	}
	if len(m.Meta) > 0 && !json.Valid(m.Meta) {
		return m, errors.New("store: message meta is not valid JSON")
	}
	if err := checkAuthor(m.Author); err != nil {
		return m, err
	}
	if m.ID == "" {
		id, err := NewID()
		if err != nil {
			return m, err
		}
		m.ID = id
	}
	m.ThreadID = threadID
	m.CreatedAt = NowIfZero(m.CreatedAt)
	m.Meta = Raw(m.Meta)
	return m, nil
}

func checkAuthor(a store.Author) error {
	switch a.Type {
	case store.AuthorHuman, store.AuthorAI, store.AuthorSystem:
	default:
		return fmt.Errorf("store: invalid author type %q", a.Type)
	}
	if a.Subject == "" {
		return errors.New("store: author subject is required")
	}
	return nil
}

// CheckStatus validates a thread status for SetStatus.
func CheckStatus(st store.ThreadStatus) error {
	switch st {
	case store.ThreadOpen, store.ThreadResolved:
		return nil
	default:
		return fmt.Errorf("store: invalid thread status %q", st)
	}
}

// CheckPrefs validates a preferences document for Prefs.Put.
func CheckPrefs(data json.RawMessage) error {
	if len(data) > MaxPrefsBytes {
		return fmt.Errorf("%w: prefs larger than %d bytes", store.ErrLimit, MaxPrefsBytes)
	}
	if !json.Valid(data) {
		return errors.New("store: prefs are not valid JSON")
	}
	return nil
}

// PrepareAudit validates and normalises an audit event for Append.
func PrepareAudit(e store.AuditEvent) (store.AuditEvent, error) {
	if len(e.Detail) > MaxAuditDetailBytes {
		return e, fmt.Errorf("%w: audit detail larger than %d bytes", store.ErrLimit, MaxAuditDetailBytes)
	}
	if len(e.Detail) > 0 && !json.Valid(e.Detail) {
		return e, errors.New("store: audit detail is not valid JSON")
	}
	switch e.Result {
	case store.AuditOK, store.AuditDenied, store.AuditError:
	default:
		return e, fmt.Errorf("store: invalid audit result %q", e.Result)
	}
	if e.Action == "" {
		return e, errors.New("store: audit action is required")
	}
	e.Time = NowIfZero(e.Time)
	e.Groups = Strings(e.Groups)
	e.Detail = Raw(e.Detail)
	return e, nil
}

// RefMatches reports whether ref matches filter, where empty filter fields
// are wildcards.
func RefMatches(filter, ref store.ResourceRef) bool {
	return (filter.Cluster == "" || filter.Cluster == ref.Cluster) &&
		(filter.Group == "" || filter.Group == ref.Group) &&
		(filter.Kind == "" || filter.Kind == ref.Kind) &&
		(filter.Namespace == "" || filter.Namespace == ref.Namespace) &&
		(filter.Name == "" || filter.Name == ref.Name)
}

// Days converts a retention in days to a duration.
func Days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// PrepareSession validates and normalises a session for Sessions.Create.
func PrepareSession(s store.Session) (store.Session, error) {
	if len(s.IDHash) == 0 {
		return s, errors.New("store: session id hash is required")
	}
	if s.Subject == "" {
		return s, errors.New("store: session subject is required")
	}
	if s.ExpiresAt.IsZero() {
		return s, errors.New("store: session expiry is required")
	}
	s.IDHash = Bytes(s.IDHash)
	s.Groups = Strings(s.Groups)
	s.CreatedAt = NowIfZero(s.CreatedAt)
	if s.LastSeenAt.IsZero() {
		s.LastSeenAt = s.CreatedAt
	}
	s.LastSeenAt = Norm(s.LastSeenAt)
	s.ExpiresAt = Norm(s.ExpiresAt)
	return s, nil
}

// PrepareToken validates and normalises a token for Tokens.Create.
func PrepareToken(t store.Token) (store.Token, error) {
	if t.ID == "" {
		return t, errors.New("store: token id is required")
	}
	if len(t.Hash) == 0 {
		return t, errors.New("store: token hash is required")
	}
	if t.Subject == "" {
		return t, errors.New("store: token subject is required")
	}
	if t.ExpiresAt.IsZero() {
		return t, errors.New("store: token expiry is required")
	}
	t.Hash = Bytes(t.Hash)
	t.Groups = Strings(t.Groups)
	t.Scopes = Strings(t.Scopes)
	t.CreatedAt = NowIfZero(t.CreatedAt)
	t.ExpiresAt = Norm(t.ExpiresAt)
	t.LastUsedAt = NormPtr(t.LastUsedAt)
	t.RevokedAt = NormPtr(t.RevokedAt)
	return t, nil
}

// CheckRateLimit validates the arguments of RateLimits.Hit.
func CheckRateLimit(key string, window time.Duration) error {
	if key == "" {
		return fmt.Errorf("%w: rate-limit key is required", store.ErrInvalid)
	}
	if window < time.Millisecond {
		return fmt.Errorf("%w: rate-limit window must be at least 1ms, got %s", store.ErrInvalid, window)
	}
	return nil
}

// PrepareAgentSession validates and normalises a session for
// AgentSessions.Upsert.
func PrepareAgentSession(s store.AgentSession) (store.AgentSession, error) {
	if s.Cluster == "" || s.HubPod == "" || s.AgentInstance == "" {
		return s, fmt.Errorf("%w: agent session cluster, hub pod and agent instance are required", store.ErrInvalid)
	}
	if s.Seq < 0 {
		return s, fmt.Errorf("%w: agent session seq must not be negative", store.ErrInvalid)
	}
	s.ConnectedAt = NowIfZero(s.ConnectedAt)
	if s.HeartbeatAt.IsZero() {
		s.HeartbeatAt = s.ConnectedAt
	}
	s.HeartbeatAt = Norm(s.HeartbeatAt)
	return s, nil
}

// EncodeEvent validates e for Events.Publish and returns its JSON form.
func EncodeEvent(e store.Event) ([]byte, error) {
	switch e.Kind {
	case store.EventThread, store.EventRevoke, store.EventAgent:
	default:
		return nil, fmt.Errorf("%w: event kind %q", store.ErrInvalid, e.Kind)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("store: encode event: %w", err)
	}
	if len(b) > store.MaxEventBytes {
		return nil, fmt.Errorf("%w: event larger than %d bytes", store.ErrLimit, store.MaxEventBytes)
	}
	return b, nil
}

// DecodeEvent parses an event produced by EncodeEvent.
func DecodeEvent(b []byte) (store.Event, error) {
	var e store.Event
	if err := json.Unmarshal(b, &e); err != nil {
		return store.Event{}, fmt.Errorf("store: decode event: %w", err)
	}
	if e.Kind == "" {
		return store.Event{}, errors.New("store: decode event: kind is missing")
	}
	return e, nil
}
