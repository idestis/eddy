package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/eddy-gitops/eddy/internal/config"
)

// anthropicProvider calls the Anthropic Messages API.
type anthropicProvider struct {
	client anthropic.Client
	model  string
}

// NewAnthropic returns a Provider for the Anthropic Messages API. The API
// key is read from the environment variable cfg.APIKeyEnv; the SDK's own
// environment defaults are disabled so nothing else is picked up silently.
func NewAnthropic(cfg config.AnthropicAI) (Provider, error) {
	if cfg.Model == "" {
		return nil, errors.New("ai: anthropic: model is required")
	}
	if cfg.APIKeyEnv == "" {
		return nil, errors.New("ai: anthropic: apiKeyEnv is required")
	}
	key := os.Getenv(cfg.APIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("ai: anthropic: environment variable %s is empty", cfg.APIKeyEnv)
	}
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(key),
		option.WithMaxRetries(2),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &anthropicProvider{client: anthropic.NewClient(opts...), model: cfg.Model}, nil
}

func (a *anthropicProvider) Name() string  { return "anthropic" }
func (a *anthropicProvider) Model() string { return a.model }

func (a *anthropicProvider) Complete(ctx context.Context, r Request) (Response, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: int64(r.MaxTokens),
		Messages:  make([]anthropic.MessageParam, 0, len(r.Messages)),
	}
	if r.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: r.System}}
	}
	for _, m := range r.Messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case BlockToolUse:
				input := b.Input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, input, b.Name))
			case BlockToolResult:
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ID, b.Content, b.IsError))
			default:
				return Response{}, fmt.Errorf("ai: anthropic: unknown block type %q", b.Type)
			}
		}
		if m.Role == RoleAssistant {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(blocks...))
		} else {
			params.Messages = append(params.Messages, anthropic.NewUserMessage(blocks...))
		}
	}
	for _, t := range r.Tools {
		props, req := schemaParts(t.InputSchema)
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: props, Required: req},
		}})
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("ai: anthropic: messages: %w", err)
	}
	out := Response{
		StopReason: anthropicStop(msg.StopReason),
		Usage:      Usage{InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens)},
	}
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			out.Content = append(out.Content, TextBlock(b.Text))
		case "tool_use":
			out.Content = append(out.Content, Block{Type: BlockToolUse, ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	return out, nil
}

func anthropicStop(s anthropic.StopReason) StopReason {
	switch s {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
		return StopEndTurn
	case anthropic.StopReasonToolUse:
		return StopToolUse
	case anthropic.StopReasonMaxTokens, anthropic.StopReasonModelContextWindowExceeded:
		return StopMaxTokens
	case anthropic.StopReasonRefusal:
		return StopRefused
	default:
		return StopOther
	}
}
