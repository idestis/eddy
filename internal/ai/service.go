package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/redact"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
)

var (
	// ErrRateLimited means the user hit the per-hour or concurrency cap (429).
	ErrRateLimited = errors.New("ai: rate limited")
	// ErrInvalid means the request is malformed (400).
	ErrInvalid = errors.New("ai: invalid request")
	// ErrUnavailable means the quota could not be checked because the store
	// is unreachable (503).
	ErrUnavailable = errors.New("ai: temporarily unavailable")
)

// Request limits that are not configurable.
const (
	// MaxQuestionBytes caps the user's question.
	MaxQuestionBytes = 8 << 10
	// historyMessages is how many earlier chat messages are sent as context.
	historyMessages = 10
	// historyMessageBytes caps each earlier message.
	historyMessageBytes = 2 << 10
	// maxHistoryPages bounds how far Ask pages through a long chat; with
	// historyPageSize it covers store.MaxMessagesPerChat.
	maxHistoryPages = 5
	historyPageSize = 200
	// minContextBytes is the smallest share of the context budget one
	// context reference gets.
	minContextBytes = 1 << 10
	// maxAuditRefLen caps one context reference in the audit detail, so
	// MaxChatContext of them fit the 4 KiB detail.
	maxAuditRefLen = 200
	// toolTimeout bounds one tool call.
	toolTimeout = 15 * time.Second
	// maxStepArgsBytes caps the recorded arguments of one step.
	maxStepArgsBytes = 1 << 10
)

// AskRequest is the body of POST /api/v1/ai/ask.
type AskRequest struct {
	// ChatID continues one of the caller's chats. Without it a new chat is
	// created with Context and a title taken from the question.
	ChatID string `json:"chatId,omitempty"`
	// Context, when set, replaces the chat's context before the question is
	// asked (nil keeps it). The hub canonicalises the references.
	Context  *[]store.ResourceRef `json:"context,omitempty"`
	Question string               `json:"question"`
	// Attachments are log lines or a YAML excerpt the user selected in the
	// UI. They are redacted, capped and wrapped as untrusted data, never
	// treated as part of the question, and never stored. Log attachments
	// need ai.allowLogs; YAML attachments do not, since get_resource already
	// exposes the same redacted YAML to the model.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is one block of user-selected log lines.
type Attachment struct {
	Kind   string   `json:"kind"`   // "logs" or "yaml"
	Source string   `json:"source"` // e.g. "apps/podinfo-7d9f/podinfo"; shown to the model as a label
	Lines  []string `json:"lines"`
}

// Attachment limits.
const (
	MaxAttachments         = 3
	MaxAttachmentLines     = 500
	MaxAttachmentBytes     = 32 << 10
	maxAttachmentSourceLen = 200
)

// AskResponse is the reply to an ask.
type AskResponse struct {
	Chat    store.Chat    `json:"chat"`
	Message store.Message `json:"message"`
	Steps   []Step        `json:"steps"`
	// ContextStatus has one entry per Chat.Context reference, in order.
	ContextStatus []ContextStatus `json:"contextStatus"`
}

// ContextStatus says whether a context reference reached the model.
type ContextStatus string

const (
	// ContextOK: the user may see the reference; its summary was sent.
	ContextOK ContextStatus = "ok"
	// ContextHidden: the user may no longer see it; nothing was sent.
	ContextHidden ContextStatus = "hidden"
)

// Step records one tool call made while answering. Args are redacted.
type Step struct {
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
	Bytes int             `json:"bytes"`
}

// Option customises a Service.
type Option func(*Service)

// WithProvider sets the model provider instead of building one from the
// config. Tests and alternative providers use it.
func WithProvider(p Provider) Option { return func(s *Service) { s.provider = p } }

// WithRateLimits keeps the hourly per-user quota in rl, shared by every hub
// replica. Without it the quota is counted in this process. The quota
// fails closed: when rl cannot be reached, Ask returns ErrUnavailable.
func WithRateLimits(rl store.RateLimits) Option { return func(s *Service) { s.rl = rl } }

// Service answers questions about the fleet as the asking user.
type Service struct {
	cfg          config.AI
	provider     Provider
	fleet        fleet.Service
	chats        store.Chats
	audit        *audit.Recorder
	flags        runtimeflags.Source
	groupForKind func(string) (string, bool)
	log          *slog.Logger
	limiter      *userLimiter
	rl           store.RateLimits
	asks         atomic.Uint64
}

// New returns an Ask AI service. When cfg.Enabled is false no provider is
// built and Ask always returns fleet.ErrDisabled. chats stores the
// conversations; every call passes the asking user as the owner.
// groupForKind resolves the API group of a kind name the model supplies (the
// hub passes a wrapper over flux.KindByName).
func New(cfg config.AI, fl fleet.Service, chats store.Chats, rec *audit.Recorder, flags runtimeflags.Source,
	groupForKind func(string) (string, bool), log *slog.Logger, opts ...Option) (*Service, error) {
	if fl == nil || chats == nil || flags == nil || groupForKind == nil {
		return nil, errors.New("ai: fleet, chats, flags and groupForKind are required")
	}
	if log == nil {
		log = slog.Default()
	}
	applyLimitDefaults(&cfg.Limits)
	s := &Service{
		cfg:          cfg,
		fleet:        fl,
		chats:        chats,
		audit:        rec,
		flags:        flags,
		groupForKind: groupForKind,
		log:          log.With("component", "ai"),
		limiter:      newUserLimiter(cfg.Limits.PerUserPerHour, 1, time.Hour, time.Now),
	}
	for _, o := range opts {
		o(s)
	}
	if s.rl != nil {
		// The hourly quota is global; only the concurrency cap stays local.
		s.limiter = newUserLimiter(0, 1, time.Hour, time.Now)
	}
	if s.provider == nil && cfg.Enabled {
		var err error
		switch cfg.Provider {
		case "anthropic", "":
			s.provider, err = NewAnthropic(cfg.Anthropic)
		case "bedrock":
			s.provider, err = NewBedrock(context.Background(), cfg.Bedrock)
		default:
			err = fmt.Errorf("ai: unsupported provider %q", cfg.Provider)
		}
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func applyLimitDefaults(l *config.AILimits) {
	if l.MaxRounds <= 0 {
		l.MaxRounds = 6
	}
	if l.MaxToolResultBytes <= 0 {
		l.MaxToolResultBytes = 24576
	}
	if l.MaxOutputTokens <= 0 {
		l.MaxOutputTokens = 1024
	}
	if l.Timeout.Duration <= 0 {
		l.Timeout.Duration = 60 * time.Second
	}
	if l.PerUserPerHour <= 0 {
		l.PerUserPerHour = 30
	}
}

// Enabled reports whether Ask AI is configured and not switched off by the
// runtime flag. It is evaluated on every call.
func (s *Service) Enabled() bool {
	return s.cfg.Enabled && s.provider != nil && s.flags.Current().AIEnabled
}

// ProviderName is the configured provider id, shown in the UI disclosure.
func (s *Service) ProviderName() string {
	if s.provider != nil {
		return s.provider.Name()
	}
	return s.cfg.Provider
}

// Model is the model id in use, or "" when disabled.
func (s *Service) Model() string {
	if s.provider != nil {
		return s.provider.Model()
	}
	return ""
}

// Ask answers r.Question as p in one of p's chats, creating the chat when
// r.ChatID is empty. It stores the question and the answer (never the
// attachments) and returns the answer with the tool steps taken and the
// visibility of each context reference.
func (s *Service) Ask(ctx context.Context, p identity.Principal, r AskRequest) (AskResponse, error) {
	if !s.Enabled() {
		return AskResponse{}, fleet.ErrDisabled
	}
	if p.User == "" {
		return AskResponse{}, fleet.ErrForbidden
	}
	r.Question = strings.TrimSpace(r.Question)
	switch {
	case r.Question == "":
		return AskResponse{}, fmt.Errorf("%w: question is required", ErrInvalid)
	case len(r.Question) > MaxQuestionBytes:
		return AskResponse{}, fmt.Errorf("%w: question exceeds %d bytes", ErrInvalid, MaxQuestionBytes)
	case r.Context != nil && len(*r.Context) > store.MaxChatContext:
		return AskResponse{}, fmt.Errorf("%w: at most %d context references", ErrInvalid, store.MaxChatContext)
	}
	if err := s.checkAttachments(r.Attachments); err != nil {
		return AskResponse{}, err
	}

	human := p
	if human.Via == "" {
		human.Via = identity.ViaWeb
	}
	asAI := p
	asAI.Via = identity.ViaAskAI
	asAI.Client = s.provider.Model()
	run := &askRun{svc: s, human: human, asAI: asAI, req: r, nonce: newNonce()}
	run.chat.ID = r.ChatID
	if r.Context != nil {
		run.chat.Context = *r.Context
	}

	if s.asks.Add(1)%256 == 0 {
		s.limiter.sweep()
	}
	release, ok := s.limiter.acquire(p.User)
	if !ok {
		s.record(ctx, asAI, run.target(), store.AuditDenied, run.auditDetail(ErrRateLimited))
		return AskResponse{}, ErrRateLimited
	}
	defer release()
	if s.rl != nil {
		_, ok, err := s.rl.Hit(ctx, "ai:ask:"+p.User, time.Hour, s.cfg.Limits.PerUserPerHour, time.Now())
		if err != nil {
			s.log.Error("Ask AI quota unavailable; refusing", "err", err)
			s.record(ctx, asAI, run.target(), store.AuditError, run.auditDetail(ErrUnavailable))
			return AskResponse{}, ErrUnavailable
		}
		if !ok {
			s.record(ctx, asAI, run.target(), store.AuditDenied, run.auditDetail(ErrRateLimited))
			return AskResponse{}, ErrRateLimited
		}
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Limits.Timeout.Duration)
	defer cancel()

	resp, err := run.do(ctx)
	result := store.AuditOK
	switch {
	case errors.Is(err, fleet.ErrForbidden), errors.Is(err, fleet.ErrNotFound):
		result = store.AuditDenied
	case err != nil:
		result = store.AuditError
	}
	s.record(ctx, asAI, run.target(), result, run.auditDetail(err))
	if err != nil {
		return AskResponse{}, err
	}
	return resp, nil
}

func (s *Service) record(ctx context.Context, p identity.Principal, target store.ResourceRef, res store.AuditResult, detail any) {
	if s.audit != nil {
		s.audit.Record(ctx, p, "ai.ask", target, res, detail)
	}
}

func (s *Service) allowLogs() bool { return s.cfg.AllowLogs }

// askRun holds the state of one Ask.
type askRun struct {
	svc   *Service
	human identity.Principal
	asAI  identity.Principal
	req   AskRequest
	nonce string

	chat       store.Chat
	history    []store.Message
	status     []ContextStatus
	summaries  []contextSummary // visible references only, in context order
	cluster    string           // default cluster for tools
	steps      []Step
	usage      Usage
	rounds     int
	redactions int
	bytesSent  int
	stop       StopReason
}

// contextSummary is what the model is told about one visible context
// reference: the cache summary of a resource, or a cluster's overview.
type contextSummary struct {
	Ref     store.ResourceRef  `json:"ref"`
	Cluster *model.ClusterInfo `json:"cluster,omitempty"`
	Summary *model.Resource    `json:"resource,omitempty"`
	Error   string             `json:"error,omitempty"`
}

func (a *askRun) do(ctx context.Context) (AskResponse, error) {
	s := a.svc
	if err := a.openChat(ctx); err != nil {
		return AskResponse{}, err
	}
	a.checkContext(ctx)

	question := store.Message{
		Author:    store.Author{Type: store.AuthorHuman, Subject: a.human.User, Display: a.human.Display, Via: string(a.human.Via)},
		Body:      a.req.Question,
		CreatedAt: time.Now(),
	}
	if _, err := s.chats.AddMessage(ctx, a.human.User, a.chat.ID, question); err != nil {
		return AskResponse{}, chatErr("add question", err)
	}

	answer, err := a.loop(ctx)
	if err != nil {
		return AskResponse{}, err
	}

	meta, err := json.Marshal(map[string]any{
		"provider":   s.provider.Name(),
		"model":      s.provider.Model(),
		"rounds":     a.rounds,
		"steps":      a.steps,
		"usage":      a.usage,
		"redactions": a.redactions,
		"stopReason": a.stop,
	})
	if err != nil {
		return AskResponse{}, fmt.Errorf("ai: encode meta: %w", err)
	}
	// The answer is written for the asking human (the owner) via askai.
	aiAuthor := store.Author{
		Type:    store.AuthorAI,
		Subject: a.asAI.User,
		Display: a.asAI.Display,
		Via:     string(identity.ViaAskAI),
		Client:  s.provider.Model(),
	}
	wctx := context.WithoutCancel(ctx)
	msg, err := s.chats.AddMessage(wctx, a.human.User, a.chat.ID, store.Message{Author: aiAuthor, Body: answer, Meta: meta, CreatedAt: time.Now()})
	if err != nil {
		return AskResponse{}, chatErr("store answer", err)
	}
	if fresh, err := s.chats.Get(wctx, a.human.User, a.chat.ID); err == nil {
		a.chat = fresh
	} else {
		a.chat.MessageCount += 2
		a.chat.UpdatedAt = msg.CreatedAt
	}
	steps := a.steps
	if steps == nil {
		steps = []Step{}
	}
	return AskResponse{Chat: a.chat, Message: msg, Steps: steps, ContextStatus: a.status}, nil
}

// openChat loads the caller's chat (with its recent history) and applies a
// new context, or creates the chat. Another user's chat is ErrNotFound.
func (a *askRun) openChat(ctx context.Context) error {
	s := a.svc
	owner := a.human.User
	if a.req.ChatID == "" {
		c, err := s.chats.Create(ctx, store.Chat{Owner: owner, Title: chatTitle(a.req.Question), Context: a.chat.Context})
		if err != nil {
			return chatErr("create chat", err)
		}
		a.chat = c
		return nil
	}
	c, err := s.chats.Get(ctx, owner, a.req.ChatID)
	if err != nil {
		return chatErr("load chat", err)
	}
	a.chat = c
	if c.MessageCount+2 > store.MaxMessagesPerChat {
		return fmt.Errorf("ai: chat is full (%d messages); start a new chat: %w", store.MaxMessagesPerChat, store.ErrConflict)
	}
	if err := a.loadHistory(ctx); err != nil {
		return err
	}
	var u store.ChatUpdate
	if a.req.Context != nil {
		refs := *a.req.Context
		u.Context = &refs
	}
	if c.Title == "" {
		// A chat created empty (POST /api/v1/ai/chats) is named by its
		// first question.
		title := chatTitle(a.req.Question)
		u.Title = &title
	}
	if u.Context != nil || u.Title != nil {
		if c, err = s.chats.Update(ctx, owner, a.chat.ID, u); err != nil {
			return chatErr("update chat", err)
		}
		a.chat = c
	}
	return nil
}

// loadHistory keeps the last historyMessages messages of the chat.
func (a *askRun) loadHistory(ctx context.Context) error {
	var all []store.Message
	cursor := ""
	for range maxHistoryPages {
		msgs, next, err := a.svc.chats.Messages(ctx, a.human.User, a.chat.ID, cursor, historyPageSize)
		if err != nil {
			return chatErr("load history", err)
		}
		all = append(all, msgs...)
		if len(all) > historyMessages {
			all = append(all[:0:0], all[len(all)-historyMessages:]...)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	a.history = all
	return nil
}

// checkContext decides, as the asking user, which context references are
// still visible, and fetches the summaries of those that are. Anything it
// cannot decide is treated as hidden (fail closed).
func (a *askRun) checkContext(ctx context.Context) {
	s := a.svc
	refs := a.chat.Context
	a.status = make([]ContextStatus, len(refs))
	var (
		clusters map[string]model.ClusterInfo
		listErr  error
	)
	for i, ref := range refs {
		a.status[i] = ContextHidden
		sum := contextSummary{Ref: ref}
		if ref.Kind == "" {
			if clusters == nil && listErr == nil {
				var list []model.ClusterInfo
				if list, listErr = s.fleet.Clusters(ctx, a.asAI); listErr != nil {
					s.log.DebugContext(ctx, "ai context: list clusters", "err", listErr)
				}
				clusters = make(map[string]model.ClusterInfo, len(list))
				for _, ci := range list {
					clusters[ci.Name] = ci
				}
			}
			ci, ok := clusters[ref.Cluster]
			if !ok {
				continue
			}
			sum.Cluster = &ci
		} else {
			mref := toModelRef(ref)
			ok, err := s.fleet.CanGet(ctx, a.asAI, ref.Cluster, mref)
			if err != nil && !isDenied(err) {
				s.log.DebugContext(ctx, "ai context: access check", "cluster", ref.Cluster, "kind", ref.Kind, "err", err)
			}
			if err != nil || !ok {
				continue
			}
			res, err := s.fleet.Get(ctx, a.asAI, ref.Cluster, mref)
			switch {
			case errors.Is(err, fleet.ErrForbidden):
				continue
			case err != nil:
				sum.Error = errorText(err)
			default:
				sum.Summary = &res
			}
		}
		a.status[i] = ContextOK
		a.summaries = append(a.summaries, sum)
		if a.cluster == "" {
			a.cluster = ref.Cluster
		}
	}
}

// chatErr maps store errors of the caller's chat: an unknown or foreign chat
// is fleet.ErrNotFound (404), invalid input ErrInvalid (400); limits keep
// their store error (409).
func chatErr(op string, err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("ai: %s: chat %w", op, fleet.ErrNotFound)
	case errors.Is(err, store.ErrInvalid):
		return fmt.Errorf("%w: %s: %v", ErrInvalid, op, err)
	}
	return fmt.Errorf("ai: %s: %w", op, err)
}

// isDenied reports fleet errors that simply mean "no".
func isDenied(err error) bool {
	return errors.Is(err, fleet.ErrNotFound) || errors.Is(err, fleet.ErrForbidden)
}

// chatTitle is the first question on one line, cut to store.MaxTitleLen
// characters on a word boundary when it is longer.
func chatTitle(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if utf8.RuneCountInString(q) <= store.MaxTitleLen {
		return q
	}
	r := []rune(q)[:store.MaxTitleLen-1] // room for the ellipsis
	if i := lastSpace(r); i > len(r)/2 {
		r = r[:i]
	}
	return strings.TrimSpace(string(r)) + "…"
}

func lastSpace(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == ' ' {
			return i
		}
	}
	return -1
}

func (a *askRun) loop(ctx context.Context) (string, error) {
	s := a.svc
	env := toolEnv{fleet: s.fleet, principal: a.asAI, cluster: a.cluster, groupForKind: s.groupForKind}
	defs := toolDefs(s.allowLogs())
	prompt := a.userPrompt()
	a.bytesSent += len(prompt)
	msgs := []Message{{Role: RoleUser, Content: []Block{TextBlock(prompt)}}}
	system := systemPrompt(a.nonce, s.allowLogs())
	maxRounds := s.cfg.Limits.MaxRounds

	var answer []string
	for a.rounds < maxRounds {
		a.rounds++
		resp, err := s.provider.Complete(ctx, Request{System: system, Messages: msgs, Tools: defs, MaxTokens: s.cfg.Limits.MaxOutputTokens})
		if err != nil {
			return "", fmt.Errorf("ai: %s: %w", s.provider.Name(), err)
		}
		a.usage = a.usage.Add(resp.Usage)
		a.stop = resp.StopReason
		msgs = append(msgs, Message{Role: RoleAssistant, Content: resp.Content})

		answer = answer[:0]
		var uses []Block
		for _, b := range resp.Content {
			switch b.Type {
			case BlockText:
				answer = append(answer, b.Text)
			case BlockToolUse:
				uses = append(uses, b)
			}
		}
		if resp.StopReason != StopToolUse || len(uses) == 0 {
			break
		}

		results := make([]Block, 0, len(uses)+1)
		for _, u := range uses {
			results = append(results, a.runTool(ctx, env, u))
		}
		if a.rounds == maxRounds-1 {
			results = append(results, TextBlock("Tool budget exhausted: answer now using only the data above."))
		}
		msgs = append(msgs, Message{Role: RoleUser, Content: results})
	}
	return a.finish(strings.TrimSpace(strings.Join(answer, "\n\n"))), nil
}

// finish redacts and caps the answer and explains empty or cut answers.
func (a *askRun) finish(text string) string {
	switch {
	case text == "" && a.stop == StopRefused:
		text = "The model declined to answer this question."
	case text == "" && a.stop == StopToolUse:
		text = "I ran out of tool rounds before I could answer. Try a narrower question."
	case text == "":
		text = "I could not produce an answer."
	}
	if a.stop == StopMaxTokens {
		text += "\n\n_(The answer was cut off at the output limit.)_"
	}
	text, n := redact.Text(text)
	a.redactions += n
	text, _ = truncate(text, store.MaxMessageBytes)
	return text
}

func (a *askRun) runTool(ctx context.Context, env toolEnv, u Block) Block {
	tctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	out, err := dispatch(tctx, env, a.svc.allowLogs(), u.Name, u.Input)
	isErr := err != nil
	var body string
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": errorText(err)})
		body = string(b)
		if !errors.Is(err, errBadArgs) && !errors.Is(err, errUnknownTool) {
			a.svc.log.DebugContext(ctx, "ai tool failed", "tool", u.Name, "err", err)
		}
	} else if b, merr := json.Marshal(out); merr != nil {
		body, isErr = `{"error":"could not encode result"}`, true
	} else {
		body = string(b)
	}
	body, n := redact.Text(body)
	a.redactions += n
	if cut, truncated := truncate(body, a.svc.cfg.Limits.MaxToolResultBytes); truncated {
		body = cut + "\n[truncated]"
	}
	wrapped := Wrap(a.nonce, "tool:"+u.Name, body)
	a.bytesSent += len(wrapped)
	a.steps = append(a.steps, Step{Tool: u.Name, Args: stepArgs(u.Input), Bytes: len(wrapped)})
	return Block{Type: BlockToolResult, ID: u.ID, Content: wrapped, IsError: isErr}
}

// stepArgs returns redacted, size-capped arguments as valid JSON.
func stepArgs(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return json.RawMessage(`{}`)
	}
	s, _ := redact.Text(string(in))
	if len(s) <= maxStepArgsBytes && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	s, _ = truncate(s, maxStepArgsBytes)
	b, _ := json.Marshal(map[string]any{"truncated": true, "raw": s})
	return b
}

// userPrompt builds the first user turn: the chat context, the earlier
// messages and the attachments as wrapped data, then the question itself.
func (a *askRun) userPrompt() string {
	var b strings.Builder
	limit := a.svc.cfg.Limits.MaxToolResultBytes
	if n := len(a.summaries); n > 0 {
		// The context shares a budget of two tool results.
		per := min(limit, max(2*limit/n, minContextBytes))
		fmt.Fprintf(&b, "The user added %d item(s) to this chat's context. Their current summaries follow as data.\n\n", n)
		for i, sum := range a.summaries {
			raw, err := json.Marshal(sum)
			if err != nil {
				continue
			}
			text, n := redact.Text(string(raw))
			a.redactions += n
			if cut, truncated := truncate(text, per); truncated {
				text = cut + "\n[truncated]"
			}
			b.WriteString(Wrap(a.nonce, fmt.Sprintf("context:%d", i+1), text))
			b.WriteString("\n\n")
		}
	} else {
		b.WriteString("This chat has no context; use the tools to find what the question is about, passing the cluster explicitly.\n\n")
	}
	if hidden := len(a.status) - len(a.summaries); hidden > 0 {
		fmt.Fprintf(&b, "%d context item(s) are no longer visible to the user and were left out.\n\n", hidden)
	}
	if len(a.history) > 0 {
		var h strings.Builder
		for _, m := range a.history {
			body, _ := truncate(m.Body, historyMessageBytes)
			fmt.Fprintf(&h, "[%s] %s\n", m.Author.Type, body)
		}
		text, n := redact.Text(h.String())
		a.redactions += n
		text, _ = truncate(text, limit)
		b.WriteString("Earlier messages in this conversation:\n")
		b.WriteString(Wrap(a.nonce, "chat", text))
		b.WriteString("\n\n")
	}
	for _, att := range a.req.Attachments {
		raw := strings.Join(att.Lines, "\n")
		var text string
		var n int
		label := "Log lines the user selected"
		if att.Kind == "yaml" {
			text, n = redact.YAML(raw)
			label = "YAML the user selected"
		} else {
			text, n = redact.Text(raw)
		}
		a.redactions += n
		text, _ = truncate(text, MaxAttachmentBytes)
		fmt.Fprintf(&b, "%s (%s):\n", label, sanitizeLabel(att.Source))
		b.WriteString(Wrap(a.nonce, "attachment:"+att.Kind, text))
		b.WriteString("\n\n")
	}
	b.WriteString("Question from the signed-in user:\n")
	b.WriteString(a.req.Question)
	return b.String()
}

// checkAttachments enforces the attachment limits. Logs reach the model only
// when the operator allowed it (ai.allowLogs), whether fetched by a tool or
// selected by the user.
func (s *Service) checkAttachments(atts []Attachment) error {
	if len(atts) == 0 {
		return nil
	}
	if len(atts) > MaxAttachments {
		return fmt.Errorf("%w: at most %d attachments", ErrInvalid, MaxAttachments)
	}
	lines, size := 0, 0
	for _, a := range atts {
		switch a.Kind {
		case "logs":
			if !s.cfg.AllowLogs {
				return fmt.Errorf("%w: log access for Ask AI is turned off on this hub (ai.allowLogs)", ErrInvalid)
			}
		case "yaml":
		default:
			return fmt.Errorf("%w: unsupported attachment kind %q", ErrInvalid, a.Kind)
		}
		lines += len(a.Lines)
		for _, l := range a.Lines {
			size += len(l) + 1
		}
	}
	if lines > MaxAttachmentLines || size > MaxAttachmentBytes {
		return fmt.Errorf("%w: attachments exceed %d lines or %d bytes", ErrInvalid, MaxAttachmentLines, MaxAttachmentBytes)
	}
	return nil
}

// sanitizeLabel keeps an attachment label to a short, printable string, since
// it appears outside the data block.
func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '<' || r == '>' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" {
		return "no source"
	}
	if len(s) > maxAttachmentSourceLen {
		s = s[:maxAttachmentSourceLen]
	}
	return s
}

// target is the audit target of the ask: the first context reference, if any.
func (a *askRun) target() store.ResourceRef {
	if len(a.chat.Context) > 0 {
		return a.chat.Context[0]
	}
	return store.ResourceRef{}
}

func (a *askRun) auditDetail(err error) map[string]any {
	names := make([]string, 0, len(a.steps))
	for _, st := range a.steps {
		names = append(names, st.Tool)
	}
	refs := make([]string, len(a.chat.Context))
	for i, r := range a.chat.Context {
		refs[i], _ = truncate(auditRef(r), maxAuditRefLen)
	}
	d := map[string]any{
		"provider":   a.svc.provider.Name(),
		"model":      a.svc.provider.Model(),
		"rounds":     a.rounds,
		"tools":      names,
		"bytes":      a.bytesSent,
		"redactions": a.redactions,
		"usage":      a.usage,
		"context":    refs,
	}
	if a.chat.ID != "" {
		d["chatId"] = a.chat.ID
	}
	if hidden := len(a.status) - len(a.summaries); hidden > 0 {
		d["contextHidden"] = hidden
	}
	if a.stop != "" {
		d["stopReason"] = a.stop
	}
	switch {
	case errors.Is(err, ErrRateLimited):
		d["reason"] = "rate_limited"
	case errors.Is(err, ErrUnavailable):
		d["reason"] = "quota_unavailable"
	case err != nil:
		d["error"] = errorText(err)
	}
	return d
}

// auditRef is a compact form of a context reference: "cluster" for a whole
// cluster, else "cluster/group/kind/namespace/name".
func auditRef(r store.ResourceRef) string {
	if r.Kind == "" {
		return r.Cluster
	}
	return r.Cluster + "/" + r.Group + "/" + r.Kind + "/" + r.Namespace + "/" + r.Name
}

func toModelRef(r store.ResourceRef) model.Ref {
	return model.Ref{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}
