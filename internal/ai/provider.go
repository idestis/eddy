// Package ai implements Ask AI: a read-only, RBAC-scoped tool loop over a
// language model (ADR-0003 §6).
//
// The model sees only data that the asking user may read, fetched through
// fleet.Service with that user's principal. Every piece of cluster or thread
// content is redacted, size capped and wrapped in nonce-delimited
// <eddy_data> tags that the system prompt declares untrusted. The tool list
// contains no write tool, and the dispatcher rejects any name it does not
// know.
//
// Providers are pluggable behind Provider: the Anthropic Messages API and
// Amazon Bedrock Converse are built in.
package ai

import (
	"context"
	"encoding/json"
)

// Role is the speaker of a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType discriminates Block.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// Block is one provider-neutral content block.
type Block struct {
	Type BlockType `json:"type"`
	// Text is set for text blocks.
	Text string `json:"text,omitempty"`
	// ID is the tool-use id, for tool_use and tool_result blocks.
	ID string `json:"id,omitempty"`
	// Name and Input are set for tool_use blocks.
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// Content and IsError are set for tool_result blocks.
	Content string `json:"content,omitempty"`
	IsError bool   `json:"isError,omitempty"`
}

// TextBlock returns a text block.
func TextBlock(s string) Block { return Block{Type: BlockText, Text: s} }

// Message is one conversational turn.
type Message struct {
	Role    Role    `json:"role"`
	Content []Block `json:"content"`
}

// ToolDef describes a tool to the model. InputSchema is a JSON Schema object
// with "type": "object", "properties" and optionally "required".
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Request is one model turn.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

// StopReason says why the model stopped.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	// StopRefused covers refusals, guardrail interventions and content filters.
	StopRefused StopReason = "refused"
	StopOther   StopReason = "other"
)

// Usage counts tokens for one or more turns.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Add returns u plus o.
func (u Usage) Add(o Usage) Usage {
	return Usage{InputTokens: u.InputTokens + o.InputTokens, OutputTokens: u.OutputTokens + o.OutputTokens}
}

// Response is the model's reply to one Request.
type Response struct {
	Content    []Block
	StopReason StopReason
	Usage      Usage
}

// Provider runs one model turn. Implementations must be safe for
// concurrent use and must never log request content or credentials.
type Provider interface {
	Complete(ctx context.Context, r Request) (Response, error)
	// Name is the provider id: "anthropic" or "bedrock".
	Name() string
	// Model is the model or inference profile id, recorded on AI messages.
	Model() string
}

// schemaParts splits a ToolDef schema into properties and required names.
func schemaParts(s map[string]any) (props any, required []string) {
	props = s["properties"]
	if props == nil {
		props = map[string]any{}
	}
	switch r := s["required"].(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			if str, ok := v.(string); ok {
				required = append(required, str)
			}
		}
	}
	return props, required
}
