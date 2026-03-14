package compat

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openblocklabs/obl-compat-proxy/internal/modelmap"
	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
	"github.com/openblocklabs/obl-compat-proxy/internal/obl"
)

func mustRegistry(t *testing.T) *modelmap.Registry {
	t.Helper()
	registry, err := modelmap.LoadRegistry("")
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return registry
}

func TestParseOpenAIRequestMapsThinkingAndImage(t *testing.T) {
	registry := mustRegistry(t)
	body := []byte(`{
		"model":"openai/gpt-5.3-codex",
		"stream":true,
		"stream_options":{"include_usage":true},
		"reasoning_effort":"xhigh",
		"messages":[
			{"role":"system","content":"be precise"},
			{"role":"user","content":[
				{"type":"text","text":"describe","cache_control":{"type":"ephemeral","ttl":"1h"}},
				{"type":"image_url","image_url":{"url":"https://example.com/image.png"},"cache_control":{"type":"ephemeral"}}
			]}
		]
	}`)

	req, err := ParseOpenAIRequest(body, registry, 1<<20)
	if err != nil {
		t.Fatalf("ParseOpenAIRequest: %v", err)
	}

	if req.MaxTokens != 20000 {
		t.Fatalf("MaxTokens = %d, want 20000", req.MaxTokens)
	}
	if !req.IncludeUsage || !req.Stream {
		t.Fatalf("stream flags not preserved: stream=%v include_usage=%v", req.Stream, req.IncludeUsage)
	}
	if req.Thinking.Mode != normalize.ThinkingModeBudget {
		t.Fatalf("Thinking.Mode = %q, want %q", req.Thinking.Mode, normalize.ThinkingModeBudget)
	}
	if req.Thinking.BudgetTokens != normalize.DefaultBudgetForEffort(normalize.ThinkingEffortHigh)*2 {
		t.Fatalf("Thinking.BudgetTokens = %d", req.Thinking.BudgetTokens)
	}
	if got := len(req.Messages); got != 2 {
		t.Fatalf("Messages len = %d, want 2", got)
	}
	if got := len(req.Messages[1].Content); got != 2 {
		t.Fatalf("user content parts len = %d, want 2", got)
	}
	if req.Messages[1].Content[1].Type != normalize.ContentPartImage {
		t.Fatalf("second content part type = %q, want image", req.Messages[1].Content[1].Type)
	}
	if req.Messages[1].Content[0].CacheControl == nil || req.Messages[1].Content[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("text block cache_control missing: %+v", req.Messages[1].Content[0].CacheControl)
	}
	if req.Messages[1].Content[1].CacheControl == nil || req.Messages[1].Content[1].CacheControl.Type != "ephemeral" {
		t.Fatalf("image block cache_control missing: %+v", req.Messages[1].Content[1].CacheControl)
	}
}

func TestParseOpenAIRequestDowngradesBudgetToEffortForGPT54(t *testing.T) {
	registry := mustRegistry(t)
	body := []byte(`{
		"model":"openai/gpt-5.4",
		"messages":[{"role":"user","content":"hi"}],
		"reasoning":{"budget_tokens":12000}
	}`)

	req, err := ParseOpenAIRequest(body, registry, 1<<20)
	if err != nil {
		t.Fatalf("ParseOpenAIRequest: %v", err)
	}
	if req.Thinking.Mode != normalize.ThinkingModeEffort {
		t.Fatalf("Thinking.Mode = %q, want %q", req.Thinking.Mode, normalize.ThinkingModeEffort)
	}
	if req.Thinking.Effort != normalize.ThinkingEffortHigh {
		t.Fatalf("Thinking.Effort = %q, want %q", req.Thinking.Effort, normalize.ThinkingEffortHigh)
	}
}

func TestParseAnthropicRequestMapsSystemImageAndThinking(t *testing.T) {
	registry := mustRegistry(t)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))
	body := []byte(`{
		"model":"anthropic/claude-opus-4.6",
		"system":[{"type":"text","text":"system text","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"thinking":{"type":"enabled","budget_tokens":4096},
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}},
				{"type":"image","source":{"type":"url","url":"` + dataURL + `"},"cache_control":{"type":"ephemeral","ttl":"30m"}}
			]}
		]
	}`)

	req, err := ParseAnthropicRequest(body, registry, 1<<20)
	if err != nil {
		t.Fatalf("ParseAnthropicRequest: %v", err)
	}
	if got := len(req.Messages); got != 2 {
		t.Fatalf("Messages len = %d, want 2", got)
	}
	if req.Messages[0].Role != normalize.RoleSystem {
		t.Fatalf("first role = %q, want system", req.Messages[0].Role)
	}
	if req.Messages[1].Content[1].Type != normalize.ContentPartImage {
		t.Fatalf("second block type = %q, want image", req.Messages[1].Content[1].Type)
	}
	if req.Messages[0].Content[0].CacheControl == nil || req.Messages[0].Content[0].CacheControl.TTL != "1h" {
		t.Fatalf("system cache_control missing: %+v", req.Messages[0].Content[0].CacheControl)
	}
	if req.Messages[1].Content[1].CacheControl == nil || req.Messages[1].Content[1].CacheControl.TTL != "30m" {
		t.Fatalf("image cache_control missing: %+v", req.Messages[1].Content[1].CacheControl)
	}
	if req.Thinking.Mode != normalize.ThinkingModeBudget || req.Thinking.BudgetTokens != 4096 {
		t.Fatalf("unexpected thinking config: %+v", req.Thinking)
	}
}

func TestParseAnthropicRequestAcceptsAssistantThinkingBlocks(t *testing.T) {
	registry := mustRegistry(t)
	body := []byte(`{
		"model":"anthropic/claude-opus-4.6",
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"internal chain","signature":"sig_123"},
				{"type":"text","text":"First answer"},
				{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"a"}}
			]},
			{"role":"user","content":"next turn"}
		]
	}`)

	req, err := ParseAnthropicRequest(body, registry, 1<<20)
	if err != nil {
		t.Fatalf("ParseAnthropicRequest: %v", err)
	}
	if got := len(req.Messages); got != 2 {
		t.Fatalf("Messages len = %d, want 2", got)
	}
	if req.Messages[0].Role != normalize.RoleAssistant {
		t.Fatalf("first role = %q, want assistant", req.Messages[0].Role)
	}
	if got := len(req.Messages[0].Content); got != 2 {
		t.Fatalf("assistant content len = %d, want 2", got)
	}
	if req.Messages[0].Content[0].Type != normalize.ContentPartThinking {
		t.Fatalf("assistant first content type = %q, want thinking", req.Messages[0].Content[0].Type)
	}
	if req.Messages[0].Content[0].Signature != "sig_123" {
		t.Fatalf("assistant thinking signature = %q", req.Messages[0].Content[0].Signature)
	}
	if req.Messages[0].Content[1].Text != "First answer" {
		t.Fatalf("assistant text = %q", req.Messages[0].Content[1].Text)
	}
	if got := len(req.Messages[0].ToolCalls); got != 1 {
		t.Fatalf("assistant tool calls len = %d, want 1", got)
	}
	if req.Messages[0].ToolCalls[0].Name != "lookup" {
		t.Fatalf("assistant tool call name = %q", req.Messages[0].ToolCalls[0].Name)
	}
}

func TestBuildAnthropicResponseIncludesThinkingBlock(t *testing.T) {
	aggregate := obl.Aggregate{
		ID:                 "gen_1",
		FinishReason:       "tool_calls",
		ReasoningSignature: "sig_123",
	}
	aggregate.Reasoning.WriteString("need tool")

	resp := BuildAnthropicResponse(aggregate, "anthropic/claude-opus-4.6")
	content, ok := resp["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content type = %T", resp["content"])
	}
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	if content[0]["type"] != "thinking" {
		t.Fatalf("first block type = %v", content[0]["type"])
	}
	if content[0]["signature"] != "sig_123" {
		t.Fatalf("signature = %v", content[0]["signature"])
	}
}

func TestBuildRequestMapsThinkingFields(t *testing.T) {
	req := normalize.Request{
		Model: modelmap.ModelSpec{
			ID:            "anthropic/claude-opus-4.6",
			Family:        "anthropic",
			UpstreamModel: "anthropic/claude-opus-4.6",
		},
		Messages: []normalize.Message{{
			Role: normalize.RoleUser,
			Content: []normalize.ContentPart{{
				Type: normalize.ContentPartText,
				Text: "hello",
				CacheControl: &normalize.CacheControl{
					Type: "ephemeral",
					TTL:  "1h",
				},
			}},
		}},
		Thinking: normalize.ThinkingConfig{
			Enabled:      true,
			Mode:         normalize.ThinkingModeBudget,
			Effort:       normalize.ThinkingEffortHigh,
			BudgetTokens: 8192,
		},
		AnthropicBetas: []string{"interleaved-thinking-2025-05-14"},
	}

	upstream := obl.BuildRequest(req, true)
	if upstream.ReasoningEnabled == nil || !*upstream.ReasoningEnabled {
		t.Fatalf("ReasoningEnabled not set")
	}
	if upstream.ReasoningBudget != 8192 {
		t.Fatalf("ReasoningBudget = %d, want 8192", upstream.ReasoningBudget)
	}
	if upstream.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", upstream.ReasoningEffort)
	}
	if !upstream.Stream || upstream.StreamOptions == nil || !upstream.StreamOptions.IncludeUsage {
		t.Fatalf("stream options not set correctly: %+v", upstream.StreamOptions)
	}
	if upstream.Messages[0].Content[0].CacheControl == nil || upstream.Messages[0].Content[0].CacheControl.TTL != "1h" {
		t.Fatalf("cache_control not forwarded: %+v", upstream.Messages[0].Content[0].CacheControl)
	}
	if got := len(upstream.AnthropicBetas); got != 1 || upstream.AnthropicBetas[0] != "interleaved-thinking-2025-05-14" {
		t.Fatalf("AnthropicBetas = %+v", upstream.AnthropicBetas)
	}
}

func TestBuildRequestPreservesAnthropicThinkingParts(t *testing.T) {
	upstream := obl.BuildRequest(normalize.Request{
		Model: modelmap.ModelSpec{
			ID:            "anthropic/claude-opus-4.6",
			Family:        "anthropic",
			UpstreamModel: "anthropic/claude-opus-4.6",
		},
		Messages: []normalize.Message{{
			Role: normalize.RoleAssistant,
			Content: []normalize.ContentPart{
				{Type: normalize.ContentPartThinking, Text: "internal chain", Signature: "sig_123"},
				{Type: normalize.ContentPartText, Text: "Let me use a tool."},
			},
			ToolCalls: []normalize.ToolCall{{
				ID:        "toolu_1",
				Name:      "lookup",
				Arguments: `{"q":"a"}`,
			}},
		}},
	}, true)

	if got := len(upstream.Messages); got != 1 {
		t.Fatalf("Messages len = %d", got)
	}
	if got := len(upstream.Messages[0].Content); got != 2 {
		t.Fatalf("content len = %d", got)
	}
	if upstream.Messages[0].Content[0].Type != "thinking" {
		t.Fatalf("first content type = %q", upstream.Messages[0].Content[0].Type)
	}
	if upstream.Messages[0].Content[0].Signature != "sig_123" {
		t.Fatalf("signature = %q", upstream.Messages[0].Content[0].Signature)
	}
}

func TestBuildRequestAutoAppliesClaudePromptCaching(t *testing.T) {
	registry := mustRegistry(t)
	spec, ok := registry.Get("anthropic/claude-opus-4.6")
	if !ok {
		t.Fatal("missing claude-opus-4.6")
	}

	req := normalize.Request{
		Model: spec,
		Messages: []normalize.Message{
			{
				Role: normalize.RoleSystem,
				Content: []normalize.ContentPart{{
					Type: normalize.ContentPartText,
					Text: "system prefix",
				}},
			},
			{
				Role: normalize.RoleUser,
				Content: []normalize.ContentPart{{
					Type: normalize.ContentPartText,
					Text: "stable context",
				}},
			},
			{
				Role: normalize.RoleUser,
				Content: []normalize.ContentPart{{
					Type: normalize.ContentPartText,
					Text: "current prompt",
				}},
			},
		},
	}

	upstream := obl.BuildRequest(req, true)
	if got, want := upstream.Models, []string{"anthropic/claude-opus-4.6", "openai/gpt-5.3-codex", "google/gemini-3.1-pro-preview"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Models = %#v, want %#v", got, want)
	}
	for i, message := range upstream.Messages {
		if message.Content[0].CacheControl == nil {
			t.Fatalf("message %d cache_control missing", i)
		}
		if message.Content[0].CacheControl.Type != "ephemeral" || message.Content[0].CacheControl.TTL != "1h" {
			t.Fatalf("message %d cache_control = %+v", i, message.Content[0].CacheControl)
		}
	}
}

func TestBuildRequestDoesNotOverrideExplicitCacheControl(t *testing.T) {
	registry := mustRegistry(t)
	spec, ok := registry.Get("anthropic/claude-opus-4.6")
	if !ok {
		t.Fatal("missing claude-opus-4.6")
	}

	req := normalize.Request{
		Model: spec,
		Messages: []normalize.Message{
			{
				Role: normalize.RoleSystem,
				Content: []normalize.ContentPart{{
					Type: normalize.ContentPartText,
					Text: "system prefix",
					CacheControl: &normalize.CacheControl{
						Type: "ephemeral",
						TTL:  "30m",
					},
				}},
			},
			{
				Role: normalize.RoleUser,
				Content: []normalize.ContentPart{{
					Type: normalize.ContentPartText,
					Text: "user prompt",
				}},
			},
		},
	}

	upstream := obl.BuildRequest(req, true)
	if upstream.Messages[0].Content[0].CacheControl == nil || upstream.Messages[0].Content[0].CacheControl.TTL != "30m" {
		t.Fatalf("explicit cache_control lost: %+v", upstream.Messages[0].Content[0].CacheControl)
	}
	if upstream.Messages[1].Content[0].CacheControl != nil {
		t.Fatalf("unexpected auto cache_control on second message: %+v", upstream.Messages[1].Content[0].CacheControl)
	}
}

func TestRewriteOpenAIChunkRewritesModel(t *testing.T) {
	raw := []byte(`{
		"id":"abc",
		"object":"chat.completion.chunk",
		"created":123,
		"model":"anthropic/claude-opus-4.6",
		"provider":"openrouter",
		"choices":[{"index":0,"delta":{"role":"assistant","reasoning":"step","reasoning_details":[{"type":"reasoning.summary","summary":"step"}]}}]
	}`)
	payload, err := RewriteOpenAIChunk(raw, "openai/gpt-5.4")
	if err != nil {
		t.Fatalf("RewriteOpenAIChunk: %v", err)
	}
	if payload["model"] != "openai/gpt-5.4" {
		t.Fatalf("model = %v, want rewritten model", payload["model"])
	}
	if _, ok := payload["provider"]; ok {
		t.Fatalf("provider should be removed")
	}
	choices := payload["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["reasoning"] != "step" {
		t.Fatalf("reasoning = %v, want step", delta["reasoning"])
	}
}

func TestBuildAnthropicResponseUsesAnthropicStopReason(t *testing.T) {
	aggregate := obl.Aggregate{
		ID:           "123",
		FinishReason: "tool_calls",
		Usage: normalize.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
		},
	}
	aggregate.Content.WriteString("done")
	resp := BuildAnthropicResponse(aggregate, "anthropic/claude-sonnet-4.6")
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}
	if !strings.Contains(string(raw), `"stop_reason":"tool_use"`) {
		t.Fatalf("response missing tool_use stop_reason: %s", raw)
	}
}

func TestBuildOpenAIUsagePreservesDetailedCounters(t *testing.T) {
	usage := normalize.Usage{
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		Extra: map[string]any{
			"prompt_tokens_details": map[string]any{
				"cached_tokens":      3,
				"cache_write_tokens": 5,
			},
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": 7,
			},
			"cost": 0.42,
		},
	}

	payload := BuildOpenAIUsage(usage)
	if payload["prompt_tokens"] != 10 || payload["completion_tokens"] != 20 || payload["total_tokens"] != 30 {
		t.Fatalf("base usage fields missing: %#v", payload)
	}
	if _, ok := payload["prompt_tokens_details"]; !ok {
		t.Fatalf("prompt_tokens_details missing: %#v", payload)
	}
	if _, ok := payload["completion_tokens_details"]; !ok {
		t.Fatalf("completion_tokens_details missing: %#v", payload)
	}
	if payload["cost"] != 0.42 {
		t.Fatalf("cost missing: %#v", payload)
	}
}

func TestBuildAnthropicUsageMapsCacheAndReasoningCounts(t *testing.T) {
	usage := normalize.Usage{
		PromptTokens:     10,
		CompletionTokens: 20,
		Extra: map[string]any{
			"prompt_tokens_details": map[string]any{
				"cached_tokens":      3,
				"cache_write_tokens": 5,
				"audio_tokens":       2,
			},
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": 7,
				"audio_tokens":     1,
				"image_tokens":     4,
			},
			"is_byok": false,
		},
	}

	payload := BuildAnthropicUsage(usage)
	want := map[string]any{
		"input_tokens":                10,
		"output_tokens":               20,
		"cache_read_input_tokens":     3,
		"cache_creation_input_tokens": 5,
		"input_audio_tokens":          2,
		"reasoning_tokens":            7,
		"output_audio_tokens":         1,
		"output_image_tokens":         4,
		"is_byok":                     false,
	}
	for key, expected := range want {
		if payload[key] != expected {
			t.Fatalf("%s = %#v, want %#v (payload=%#v)", key, payload[key], expected, payload)
		}
	}
}
