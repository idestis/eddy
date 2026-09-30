package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/idestis/eddy/internal/config"
)

type fakeConverser struct {
	in  *bedrockruntime.ConverseInput
	out *bedrockruntime.ConverseOutput
}

func (f *fakeConverser) Converse(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	f.in = in
	return f.out, nil
}

var testReq = Request{
	System:    "sys",
	MaxTokens: 256,
	Tools:     toolDefs(false),
	Messages: []Message{
		{Role: RoleUser, Content: []Block{TextBlock("q")}},
		{Role: RoleAssistant, Content: []Block{toolUse("t1", "get_events", `{"kind":"Kustomization","name":"apps"}`)}},
		{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ID: "t1", Content: "data", IsError: true}}},
	},
}

func TestBedrockMapping(t *testing.T) {
	fc := &fakeConverser{out: &bedrockruntime.ConverseOutput{
		StopReason: types.StopReasonToolUse,
		Usage:      &types.TokenUsage{InputTokens: aws.Int32(10), OutputTokens: aws.Int32(5)},
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{Role: types.ConversationRoleAssistant, Content: []types.ContentBlock{
			&types.ContentBlockMemberText{Value: "hi"},
			&types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{ToolUseId: aws.String("t2"), Name: aws.String("get_resource"), Input: document.NewLazyDocument(map[string]any{"name": "apps"})}},
		}}},
	}}
	p := newBedrock(fc, config.BedrockAI{ModelID: "eu.anthropic.claude-haiku-4-5-20251001-v1:0", Guardrail: config.BedrockGuardrail{ID: "gr-1", Version: "2", Trace: true}})
	resp, err := p.Complete(context.Background(), testReq)
	if err != nil {
		t.Fatal(err)
	}
	in := fc.in
	if aws.ToString(in.ModelId) != "eu.anthropic.claude-haiku-4-5-20251001-v1:0" || aws.ToInt32(in.InferenceConfig.MaxTokens) != 256 {
		t.Errorf("input = %+v", in)
	}
	if g := in.GuardrailConfig; g == nil || aws.ToString(g.GuardrailIdentifier) != "gr-1" || aws.ToString(g.GuardrailVersion) != "2" || g.Trace != types.GuardrailTraceEnabled {
		t.Errorf("guardrail = %+v", in.GuardrailConfig)
	}
	if len(in.ToolConfig.Tools) != 3 || len(in.Messages) != 3 || in.Messages[1].Role != types.ConversationRoleAssistant {
		t.Errorf("tools=%d messages=%d", len(in.ToolConfig.Tools), len(in.Messages))
	}
	tr := in.Messages[2].Content[0].(*types.ContentBlockMemberToolResult).Value
	if aws.ToString(tr.ToolUseId) != "t1" || tr.Status != types.ToolResultStatusError {
		t.Errorf("tool result = %+v", tr)
	}
	if resp.StopReason != StopToolUse || resp.Usage.InputTokens != 10 || len(resp.Content) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	if u := resp.Content[1]; u.ID != "t2" || u.Name != "get_resource" || !strings.Contains(string(u.Input), `"apps"`) {
		t.Errorf("tool use = %+v", u)
	}

	noGuard := newBedrock(fc, config.BedrockAI{ModelID: "m"})
	if _, err := noGuard.Complete(context.Background(), testReq); err != nil || fc.in.GuardrailConfig != nil {
		t.Errorf("guardrail set without id: %v", err)
	}
}

func TestAnthropicMapping(t *testing.T) {
	var body map[string]any
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m",
			"content":[{"type":"text","text":"answer"},{"type":"tool_use","id":"tu9","name":"get_events","input":{"name":"apps"}}],
			"stop_reason":"tool_use","usage":{"input_tokens":7,"output_tokens":3}}`)
	}))
	defer srv.Close()
	t.Setenv("EDDY_TEST_ANTHROPIC_KEY", "sk-ant-test")
	p, err := NewAnthropic(config.AnthropicAI{Model: "claude-haiku-4-5-20251001", APIKeyEnv: "EDDY_TEST_ANTHROPIC_KEY", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), testReq)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "sk-ant-test" {
		t.Errorf("api key header = %q", gotKey)
	}
	if body["model"] != "claude-haiku-4-5-20251001" || body["max_tokens"].(float64) != 256 || len(body["tools"].([]any)) != 3 || len(body["messages"].([]any)) != 3 {
		t.Errorf("body = %v", body)
	}
	if resp.StopReason != StopToolUse || resp.Usage.OutputTokens != 3 || len(resp.Content) != 2 || resp.Content[1].ID != "tu9" {
		t.Errorf("resp = %+v", resp)
	}

	if _, err := NewAnthropic(config.AnthropicAI{Model: "m", APIKeyEnv: "EDDY_TEST_UNSET_KEY"}); err == nil {
		t.Error("missing key accepted")
	}
}
