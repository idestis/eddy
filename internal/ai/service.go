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

	"github.com/eddy-gitops/eddy/internal/audit"
	"github.com/eddy-gitops/eddy/internal/config"
	"github.com/eddy-gitops/eddy/internal/fleet"
	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/redact"
	"github.com/eddy-gitops/eddy/internal/runtimeflags"
	"github.com/eddy-gitops/eddy/internal/store"
)

var (
	// ErrRateLimited means the user hit the per-hour or concurrency cap (429).
	ErrRateLimited = errors.New("ai: rate limited")
	// ErrInvalid means the request is malformed (400).
	ErrInvalid = errors.New("ai: invalid request")
)

// Request limits that are not configurable.
const (
	// MaxQuestionBytes caps the user's question.
	MaxQuestionBytes = 8 << 10
	// historyMessages is how many earlier thread messages are sent as context.
	historyMessages = 10
	// historyMessageBytes caps each earlier message.
	historyMessageBytes = 2 << 10
	// maxHistoryPages bounds how far Ask pages through a long thread.
	maxHistoryPages = 5
	historyPageSize = 200
	// toolTimeout bounds one tool call.
	toolTimeout = 15 * time.Second
	// maxStepArgsBytes caps the recorded arguments of one step.
	maxStepArgsBytes = 1 << 10
	// titleRunes is the length of an ask thread title.
	titleRunes = 80
)

// AskRequest is the body of POST /api/v1/ai/ask.
type AskRequest struct {
	Cluster    string `json:"cluster"`
	ResourceID string `json:"resourceId,omitempty"`
	ThreadID   string `json:"threadId,omitempty"`
	Question   string `json:"question"`
}

// AskResponse is the reply to an ask.
type AskResponse struct {
	ThreadID string        `json:"threadId"`
	Message  store.Message `json:"message"`
	Steps    []Step        `json:"steps"`
}

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

// Service answers questions about the fleet as the asking user.
type Service struct {
	cfg          config.AI
	provider     Provider
	fleet        fleet.Service
	threads      ThreadStore
	audit        *audit.Recorder
	flags        runtimeflags.Source
	groupForKind func(string) (string, bool)
	log          *slog.Logger
	limiter      *userLimiter
	asks         atomic.Uint64
}

// New returns an Ask AI service. When cfg.Enabled is false no provider is
// built and Ask always returns fleet.ErrDisabled. groupForKind resolves the
// API group of a kind name the model supplies (the hub passes a wrapper over
// flux.KindByName).
func New(cfg config.AI, fl fleet.Service, th ThreadStore, rec *audit.Recorder, flags runtimeflags.Source,
	groupForKind func(string) (string, bool), log *slog.Logger, opts ...Option) (*Service, error) {
	if fl == nil || th == nil || flags == nil || groupForKind == nil {
		return nil, errors.New("ai: fleet, threads, flags and groupForKind are required")
	}
	if log == nil {
		log = slog.Default()
	}
	applyLimitDefaults(&cfg.Limits)
	s := &Service{
		cfg:          cfg,
		fleet:        fl,
		threads:      th,
		audit:        rec,
		flags:        flags,
		groupForKind: groupForKind,
		log:          log.With("component", "ai"),
		limiter:      newUserLimiter(cfg.Limits.PerUserPerHour, 1, time.Hour, time.Now),
	}
	for _, o := range opts {
		o(s)
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

// Ask answers r.Question as p. It stores the question and the answer in a
// private "ask" thread and returns the answer with the tool steps taken.
func (s *Service) Ask(ctx context.Context, p identity.Principal, r AskRequest) (AskResponse, error) {
	if !s.Enabled() {
		return AskResponse{}, fleet.ErrDisabled
	}
	r.Question = strings.TrimSpace(r.Question)
	switch {
	case r.Cluster == "":
		return AskResponse{}, fmt.Errorf("%w: cluster is required", ErrInvalid)
	case r.Question == "":
		return AskResponse{}, fmt.Errorf("%w: question is required", ErrInvalid)
	case len(r.Question) > MaxQuestionBytes:
		return AskResponse{}, fmt.Errorf("%w: question exceeds %d bytes", ErrInvalid, MaxQuestionBytes)
	}

	human := p
	if human.Via == "" {
		human.Via = identity.ViaWeb
	}
	asAI := p
	asAI.Via = identity.ViaAskAI
	asAI.Client = s.provider.Model()

	if s.asks.Add(1)%256 == 0 {
		s.limiter.sweep()
	}
	release, ok := s.limiter.acquire(p.User)
	if !ok {
		s.record(ctx, asAI, store.ResourceRef{Cluster: r.Cluster}, store.AuditDenied, map[string]any{"reason": "rate_limited"})
		return AskResponse{}, ErrRateLimited
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Limits.Timeout.Duration)
	defer cancel()

	run := &askRun{svc: s, human: human, asAI: asAI, req: r, nonce: newNonce()}
	resp, err := run.do(ctx)
	result := store.AuditOK
	switch {
	case errors.Is(err, fleet.ErrForbidden), errors.Is(err, fleet.ErrNotFound):
		result = store.AuditDenied
	case err != nil:
		result = store.AuditError
	}
	s.record(ctx, asAI, run.target, result, run.auditDetail(err))
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

	target     store.ResourceRef
	thread     store.Thread
	history    []store.Message
	summary    *model.Resource
	steps      []Step
	usage      Usage
	rounds     int
	redactions int
	bytesSent  int
	stop       StopReason
}

func (a *askRun) do(ctx context.Context) (AskResponse, error) {
	s := a.svc
	a.target = store.ResourceRef{Cluster: a.req.Cluster}
	if a.req.ResourceID != "" {
		ref, err := model.ParseRef(a.req.ResourceID)
		if err != nil {
			return AskResponse{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		a.target = toStoreRef(a.req.Cluster, ref)
	}

	if a.req.ThreadID != "" {
		if err := a.loadThread(ctx); err != nil {
			return AskResponse{}, err
		}
	}

	// The summary doubles as the RBAC check on the target: a user who
	// cannot get the resource gets ErrForbidden or ErrNotFound here.
	if a.target.Kind != "" {
		res, err := s.fleet.Get(ctx, a.asAI, a.target.Cluster, toModelRef(a.target))
		if err != nil {
			return AskResponse{}, fmt.Errorf("ai: get %s: %w", a.target.Kind, err)
		}
		a.summary = &res
	}

	author := store.Author{Type: store.AuthorHuman, Subject: a.human.User, Display: a.human.Display, Via: string(a.human.Via)}
	if a.thread.ID == "" {
		t, _, err := s.threads.Create(ctx, a.human, CreateInput{
			Ref:        a.target,
			Title:      firstRunes(a.req.Question, titleRunes),
			Body:       a.req.Question,
			Type:       store.ThreadAsk,
			Visibility: store.VisibilityPrivate,
			Author:     author,
		})
		if err != nil {
			return AskResponse{}, fmt.Errorf("ai: create thread: %w", err)
		}
		a.thread = t
	} else if _, err := s.threads.Reply(ctx, a.human, a.thread.ID, a.req.Question, author, nil); err != nil {
		return AskResponse{}, fmt.Errorf("ai: add question: %w", err)
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
	// The threads service takes the author's subject and Via from the
	// principal, so the answer is written as the askai principal.
	aiAuthor := store.Author{
		Type:    store.AuthorAI,
		Subject: a.asAI.User,
		Display: a.asAI.Display,
		Via:     string(identity.ViaAskAI),
		Client:  s.provider.Model(),
	}
	msg, err := s.threads.Reply(context.WithoutCancel(ctx), a.asAI, a.thread.ID, answer, aiAuthor, meta)
	if err != nil {
		return AskResponse{}, fmt.Errorf("ai: store answer: %w", err)
	}
	steps := a.steps
	if steps == nil {
		steps = []Step{}
	}
	return AskResponse{ThreadID: a.thread.ID, Message: msg, Steps: steps}, nil
}

// loadThread loads an existing ask thread, which must be the user's own and
// target the requested cluster (and resource, when one is given).
func (a *askRun) loadThread(ctx context.Context) error {
	var all []store.Message
	cursor := ""
	for page := 0; page < maxHistoryPages; page++ {
		t, msgs, next, err := a.svc.threads.Get(ctx, a.human, a.req.ThreadID, cursor, historyPageSize)
		if err != nil {
			return fmt.Errorf("ai: load thread: %w", err)
		}
		a.thread = t
		all = append(all, msgs...)
		if next == "" {
			break
		}
		cursor = next
	}
	t := a.thread
	if t.Type != store.ThreadAsk || t.CreatedBy.Subject != a.human.User {
		return fmt.Errorf("%w: thread is not one of your Ask AI conversations", ErrInvalid)
	}
	if t.Ref.Cluster != a.req.Cluster || a.req.ResourceID != "" && t.Ref != a.target {
		return fmt.Errorf("%w: thread belongs to a different target", ErrInvalid)
	}
	a.target = t.Ref
	if len(all) > historyMessages {
		all = all[len(all)-historyMessages:]
	}
	a.history = all
	return nil
}

func (a *askRun) loop(ctx context.Context) (string, error) {
	s := a.svc
	env := toolEnv{fleet: s.fleet, principal: a.asAI, cluster: a.target.Cluster, groupForKind: s.groupForKind}
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

// userPrompt builds the first user turn: context as wrapped data, then the
// question itself.
func (a *askRun) userPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "The user is looking at cluster %q", a.target.Cluster)
	if a.target.Kind != "" {
		fmt.Fprintf(&b, ", resource %q", toModelRef(a.target).ID())
	}
	b.WriteString(".\n\n")
	if a.summary != nil {
		if raw, err := json.Marshal(a.summary); err == nil {
			text, n := redact.Text(string(raw))
			a.redactions += n
			text, _ = truncate(text, a.svc.cfg.Limits.MaxToolResultBytes)
			b.WriteString("Current resource summary:\n")
			b.WriteString(Wrap(a.nonce, "resource", text))
			b.WriteString("\n\n")
		}
	}
	if len(a.history) > 0 {
		var h strings.Builder
		for _, m := range a.history {
			body, _ := truncate(m.Body, historyMessageBytes)
			fmt.Fprintf(&h, "[%s] %s\n", m.Author.Type, body)
		}
		text, n := redact.Text(h.String())
		a.redactions += n
		text, _ = truncate(text, a.svc.cfg.Limits.MaxToolResultBytes)
		b.WriteString("Earlier messages in this conversation:\n")
		b.WriteString(Wrap(a.nonce, "thread", text))
		b.WriteString("\n\n")
	}
	b.WriteString("Question from the signed-in user:\n")
	b.WriteString(a.req.Question)
	return b.String()
}

func (a *askRun) auditDetail(err error) map[string]any {
	names := make([]string, 0, len(a.steps))
	for _, st := range a.steps {
		names = append(names, st.Tool)
	}
	d := map[string]any{
		"provider":   a.svc.provider.Name(),
		"model":      a.svc.provider.Model(),
		"rounds":     a.rounds,
		"tools":      names,
		"bytes":      a.bytesSent,
		"redactions": a.redactions,
		"usage":      a.usage,
	}
	if a.thread.ID != "" {
		d["threadId"] = a.thread.ID
	}
	if a.stop != "" {
		d["stopReason"] = a.stop
	}
	if err != nil {
		d["error"] = errorText(err)
	}
	return d
}

func toStoreRef(cluster string, r model.Ref) store.ResourceRef {
	return store.ResourceRef{Cluster: cluster, Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}

func toModelRef(r store.ResourceRef) model.Ref {
	return model.Ref{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}
