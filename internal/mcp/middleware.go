package mcp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/redact"
	"github.com/eddy-gitops/eddy/internal/store"
)

// maxAuditArgs caps the redacted arguments stored per audit event.
const maxAuditArgs = 1 << 10

// call is the per-tools/call state shared by the middleware and the tool
// handler. The handler fills in the target, the outcome and the size.
type call struct {
	p          identity.Principal
	tool       string
	target     store.ResourceRef
	result     store.AuditResult
	reason     string
	bytes      int
	redactions int
}

type callKey struct{}

func callFrom(ctx context.Context) *call {
	c, _ := ctx.Value(callKey{}).(*call)
	return c
}

var toolNameRE = regexp.MustCompile(`^[a-z_]{1,64}$`)

// toolMiddleware authorises, rate limits, times out and audits every
// tools/call. Other methods (initialize, tools/list, ping) pass through.
func (s *server) toolMiddleware(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		ctr, ok := req.(*sdk.CallToolRequest)
		if !ok || ctr.Params == nil || ctr.Extra == nil {
			return toolError("malformed request"), nil
		}
		p, ok := principalFrom(ctr.Extra.TokenInfo)
		if !ok {
			return toolError("unauthorized"), nil
		}
		if ci := ctr.ClientInfo(); ci != nil {
			p.Client = cleanClient(ci.Name)
		}
		name := ctr.Params.Name
		if !toolNameRE.MatchString(name) {
			name = "unknown"
		}
		c := &call{p: p, tool: name, result: store.AuditOK, target: s.argTarget(ctr.Params.Arguments)}
		start := time.Now()

		var res sdk.Result
		var err error
		if !s.calls.allow(tokenKey(p)) {
			c.result, c.reason = store.AuditDenied, "rate_limited"
			res = toolError("rate limit exceeded: at most " + itoa(s.o.Config.CallsPerMinute) + " tool calls per minute for this token; wait and retry")
		} else {
			tctx, cancel := context.WithTimeout(context.WithValue(ctx, callKey{}, c), ToolTimeout)
			res, err = next(tctx, method, req)
			cancel()
		}
		switch r := res.(type) {
		case *sdk.CallToolResult:
			if r.IsError && c.result == store.AuditOK {
				c.result = store.AuditError
			}
		}
		if err != nil {
			c.result = store.AuditError
		}
		s.audit(ctx, c, ctr.Params.Arguments, time.Since(start))
		return res, err
	}
}

func (s *server) audit(ctx context.Context, c *call, args json.RawMessage, d time.Duration) {
	if s.o.Audit == nil {
		return
	}
	detail := map[string]any{
		"tool":       c.tool,
		"args":       auditArgs(args),
		"bytes":      c.bytes,
		"durationMs": d.Milliseconds(),
	}
	if c.p.Client != "" {
		detail["client"] = c.p.Client
	}
	if c.redactions > 0 {
		detail["redactions"] = c.redactions
	}
	if c.reason != "" {
		detail["reason"] = c.reason
	}
	s.o.Audit.Record(ctx, c.p, "mcp."+c.tool, c.target, c.result, detail)
}

// argTarget extracts the audit target from the raw arguments.
func (s *server) argTarget(raw json.RawMessage) store.ResourceRef {
	var a struct {
		Cluster   string `json:"cluster"`
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Pod       string `json:"pod"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil {
		return store.ResourceRef{}
	}
	t := store.ResourceRef{Cluster: clip(a.Cluster, 253), Kind: clip(a.Kind, 63), Namespace: clip(a.Namespace, 253), Name: clip(a.Name, 253)}
	if a.Pod != "" {
		t.Kind, t.Name = "Pod", clip(a.Pod, 253)
	}
	if t.Kind != "" {
		t.Group, _ = s.o.GroupForKind(t.Kind)
	}
	return t
}

// auditArgs returns the arguments redacted and capped at 1 KiB.
func auditArgs(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	text, _ := redact.Text(string(raw))
	if len(text) <= maxAuditArgs && json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	cut, _ := truncate(text, maxAuditArgs)
	return map[string]any{"truncated": true, "raw": cut}
}

// cleanClient keeps a short printable client name. It is untrusted and only
// logged and shown as a badge.
func cleanClient(s string) string {
	s = strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	return clip(strings.TrimSpace(s), 64)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func toolError(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}
