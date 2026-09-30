package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/idestis/eddy/internal/config"
)

// converser is the subset of the Bedrock runtime client used here.
type converser interface {
	Converse(ctx context.Context, in *bedrockruntime.ConverseInput, opts ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// bedrockProvider calls Amazon Bedrock Converse.
type bedrockProvider struct {
	client    converser
	model     string
	guardrail *types.GuardrailConfiguration
}

// NewBedrock returns a Provider for Amazon Bedrock Converse. Credentials come
// from the AWS default chain, which covers IRSA and EKS Pod Identity; Eddy
// never takes static keys in its own configuration.
func NewBedrock(ctx context.Context, cfg config.BedrockAI) (Provider, error) {
	if cfg.Region == "" || cfg.ModelID == "" {
		return nil, errors.New("ai: bedrock: region and modelId are required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("ai: bedrock: load aws config: %w", err)
	}
	return newBedrock(bedrockruntime.NewFromConfig(awsCfg), cfg), nil
}

func newBedrock(c converser, cfg config.BedrockAI) *bedrockProvider {
	p := &bedrockProvider{client: c, model: cfg.ModelID}
	if cfg.Guardrail.ID != "" {
		trace := types.GuardrailTraceDisabled
		if cfg.Guardrail.Trace {
			trace = types.GuardrailTraceEnabled
		}
		version := cfg.Guardrail.Version
		if version == "" {
			version = "DRAFT"
		}
		p.guardrail = &types.GuardrailConfiguration{
			GuardrailIdentifier: aws.String(cfg.Guardrail.ID),
			GuardrailVersion:    aws.String(version),
			Trace:               trace,
		}
	}
	return p
}

func (b *bedrockProvider) Name() string  { return "bedrock" }
func (b *bedrockProvider) Model() string { return b.model }

func (b *bedrockProvider) Complete(ctx context.Context, r Request) (Response, error) {
	in := &bedrockruntime.ConverseInput{
		ModelId:         aws.String(b.model),
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(r.MaxTokens))},
		GuardrailConfig: b.guardrail,
	}
	if r.System != "" {
		in.System = []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: r.System}}
	}
	for _, m := range r.Messages {
		msg := types.Message{Role: types.ConversationRoleUser}
		if m.Role == RoleAssistant {
			msg.Role = types.ConversationRoleAssistant
		}
		for _, blk := range m.Content {
			cb, err := bedrockBlock(blk)
			if err != nil {
				return Response{}, err
			}
			msg.Content = append(msg.Content, cb)
		}
		in.Messages = append(in.Messages, msg)
	}
	if len(r.Tools) > 0 {
		tc := &types.ToolConfiguration{}
		for _, t := range r.Tools {
			tc.Tools = append(tc.Tools, &types.ToolMemberToolSpec{Value: types.ToolSpecification{
				Name:        aws.String(t.Name),
				Description: aws.String(t.Description),
				InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(t.InputSchema)},
			}})
		}
		in.ToolConfig = tc
	}

	out, err := b.client.Converse(ctx, in)
	if err != nil {
		return Response{}, fmt.Errorf("ai: bedrock: converse: %w", err)
	}
	res := Response{StopReason: bedrockStop(out.StopReason)}
	if u := out.Usage; u != nil {
		res.Usage = Usage{InputTokens: int(aws.ToInt32(u.InputTokens)), OutputTokens: int(aws.ToInt32(u.OutputTokens))}
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return res, nil
	}
	for _, cb := range msg.Value.Content {
		switch v := cb.(type) {
		case *types.ContentBlockMemberText:
			res.Content = append(res.Content, TextBlock(v.Value))
		case *types.ContentBlockMemberToolUse:
			input := json.RawMessage(`{}`)
			if v.Value.Input != nil {
				raw, err := v.Value.Input.MarshalSmithyDocument()
				if err != nil {
					return Response{}, fmt.Errorf("ai: bedrock: decode tool input: %w", err)
				}
				input = raw
			}
			res.Content = append(res.Content, Block{
				Type:  BlockToolUse,
				ID:    aws.ToString(v.Value.ToolUseId),
				Name:  aws.ToString(v.Value.Name),
				Input: input,
			})
		}
	}
	return res, nil
}

func bedrockBlock(b Block) (types.ContentBlock, error) {
	switch b.Type {
	case BlockText:
		return &types.ContentBlockMemberText{Value: b.Text}, nil
	case BlockToolUse:
		var input any = map[string]any{}
		if len(b.Input) > 0 {
			if err := json.Unmarshal(b.Input, &input); err != nil {
				return nil, fmt.Errorf("ai: bedrock: encode tool input: %w", err)
			}
		}
		return &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
			ToolUseId: aws.String(b.ID),
			Name:      aws.String(b.Name),
			Input:     document.NewLazyDocument(input),
		}}, nil
	case BlockToolResult:
		status := types.ToolResultStatusSuccess
		if b.IsError {
			status = types.ToolResultStatusError
		}
		return &types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
			ToolUseId: aws.String(b.ID),
			Status:    status,
			Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: b.Content}},
		}}, nil
	default:
		return nil, fmt.Errorf("ai: bedrock: unknown block type %q", b.Type)
	}
}

func bedrockStop(s types.StopReason) StopReason {
	switch s {
	case types.StopReasonEndTurn, types.StopReasonStopSequence:
		return StopEndTurn
	case types.StopReasonToolUse:
		return StopToolUse
	case types.StopReasonMaxTokens, types.StopReasonModelContextWindowExceeded:
		return StopMaxTokens
	case types.StopReasonGuardrailIntervened, types.StopReasonContentFiltered:
		return StopRefused
	default:
		return StopOther
	}
}
