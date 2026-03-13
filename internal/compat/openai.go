package compat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openblocklabs/obl-compat-proxy/internal/modelmap"
	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
	"github.com/openblocklabs/obl-compat-proxy/internal/obl"
)

type OpenAIRequest struct {
	Model           string            `json:"model"`
	Messages        []OpenAIMessage   `json:"messages"`
	Stream          bool              `json:"stream"`
	StreamOptions   *OpenAIStreamOpts `json:"stream_options,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Tools           []OpenAITool      `json:"tools,omitempty"`
	ToolChoice      json.RawMessage   `json:"tool_choice,omitempty"`
	User            string            `json:"user,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Reasoning       *OpenAIReasoning  `json:"reasoning,omitempty"`
}

type OpenAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type OpenAIReasoning struct {
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Type         string `json:"type,omitempty"`
}

type OpenAIMessage struct {
	Role       string           `json:"role"`
	Name       string           `json:"name,omitempty"`
	Content    json.RawMessage  `json:"content"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type OpenAIToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type OpenAITool struct {
	Type     string             `json:"type"`
	Function OpenAIToolFunction `json:"function"`
}

type OpenAIToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

func ParseOpenAIRequest(body []byte, registry *modelmap.Registry, imageMaxBytes int) (normalize.Request, error) {
	var input OpenAIRequest
	if err := json.Unmarshal(body, &input); err != nil {
		return normalize.Request{}, fmt.Errorf("decode OpenAI request: %w", err)
	}

	spec, ok := registry.Get(input.Model)
	if !ok {
		return normalize.Request{}, fmt.Errorf("unsupported model %q", input.Model)
	}

	messages := make([]normalize.Message, 0, len(input.Messages))
	for _, message := range input.Messages {
		normalizedMessage, err := parseOpenAIMessage(message, imageMaxBytes)
		if err != nil {
			return normalize.Request{}, err
		}
		messages = append(messages, normalizedMessage)
	}

	tools := make([]normalize.ToolDefinition, 0, len(input.Tools))
	for _, tool := range input.Tools {
		tools = append(tools, normalize.ToolDefinition{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
		})
	}

	toolChoice, err := parseOpenAIToolChoice(input.ToolChoice)
	if err != nil {
		return normalize.Request{}, err
	}

	thinking, err := parseOpenAIThinking(input)
	if err != nil {
		return normalize.Request{}, err
	}
	thinking, err = normalize.NormalizeThinking(thinking, spec)
	if err != nil {
		return normalize.Request{}, err
	}

	return normalize.Request{
		Protocol:     "openai",
		Model:        spec,
		Messages:     messages,
		Stream:       input.Stream,
		IncludeUsage: input.StreamOptions != nil && input.StreamOptions.IncludeUsage,
		Temperature:  input.Temperature,
		TopP:         input.TopP,
		MaxTokens:    defaultMaxTokens(input.MaxTokens),
		Tools:        tools,
		ToolChoice:   toolChoice,
		User:         input.User,
		Thinking:     thinking,
	}, nil
}

func BuildOpenAIModelList(registry *modelmap.Registry) map[string]any {
	items := make([]map[string]any, 0, len(registry.All()))
	for _, spec := range registry.All() {
		items = append(items, map[string]any{
			"id":       spec.ID,
			"object":   "model",
			"created":  0,
			"owned_by": spec.Family,
		})
	}
	return map[string]any{
		"object": "list",
		"data":   items,
	}
}

func RewriteOpenAIChunk(raw []byte, requestedModel string) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out["model"] = requestedModel
	delete(out, "provider")
	return out, nil
}

func BuildOpenAIResponse(aggregate obl.Aggregate, requestedModel string) map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": aggregate.Content.String(),
	}
	if toolCalls := aggregate.OrderedToolCalls(); len(toolCalls) > 0 {
		items := make([]map[string]any, 0, len(toolCalls))
		for _, call := range toolCalls {
			items = append(items, map[string]any{
				"id":   call.ID,
				"type": "function",
				"function": map[string]any{
					"name":      call.Name,
					"arguments": call.Arguments,
				},
			})
		}
		message["tool_calls"] = items
		if aggregate.Content.String() == "" {
			message["content"] = nil
		}
	}

	return map[string]any{
		"id":      aggregate.ID,
		"object":  "chat.completion",
		"created": aggregate.Created,
		"model":   requestedModel,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": aggregate.FinishReason,
			},
		},
		"usage": BuildOpenAIUsage(aggregate.Usage),
	}
}

func parseOpenAIMessage(message OpenAIMessage, imageMaxBytes int) (normalize.Message, error) {
	out := normalize.Message{
		Role:       normalize.Role(strings.TrimSpace(message.Role)),
		Name:       message.Name,
		ToolCallID: message.ToolCallID,
	}

	parts, err := parseOpenAIContent(message.Content, imageMaxBytes)
	if err != nil {
		return normalize.Message{}, fmt.Errorf("parse %s message content: %w", message.Role, err)
	}
	out.Content = parts

	if len(message.ToolCalls) > 0 {
		out.ToolCalls = make([]normalize.ToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, normalize.ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			})
		}
	}

	return out, nil
}

func parseOpenAIContent(raw json.RawMessage, imageMaxBytes int) ([]normalize.ContentPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []normalize.ContentPart{{Type: normalize.ContentPartText, Text: text}}, nil
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("expected string or array content")
	}

	out := make([]normalize.ContentPart, 0, len(blocks))
	for _, block := range blocks {
		var blockType string
		if err := json.Unmarshal(block["type"], &blockType); err != nil {
			return nil, fmt.Errorf("content block missing type")
		}
		switch blockType {
		case "text":
			var textValue string
			if err := json.Unmarshal(block["text"], &textValue); err != nil {
				return nil, fmt.Errorf("text block missing text")
			}
			cacheControl, err := parseCacheControl(block["cache_control"])
			if err != nil {
				return nil, err
			}
			out = append(out, normalize.ContentPart{
				Type:         normalize.ContentPartText,
				Text:         textValue,
				CacheControl: cacheControl,
			})
		case "image_url":
			var image struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(block["image_url"], &image); err != nil {
				return nil, fmt.Errorf("image_url block missing image_url")
			}
			imageURL, mediaType, err := normalize.ValidateImageURL(image.URL, imageMaxBytes)
			if err != nil {
				return nil, err
			}
			cacheControl, err := parseCacheControl(block["cache_control"])
			if err != nil {
				return nil, err
			}
			out = append(out, normalize.ContentPart{
				Type:         normalize.ContentPartImage,
				ImageURL:     imageURL,
				MediaType:    mediaType,
				CacheControl: cacheControl,
			})
		default:
			return nil, fmt.Errorf("unsupported OpenAI content block type %q", blockType)
		}
	}

	return out, nil
}

func parseOpenAIToolChoice(raw json.RawMessage) (*normalize.ToolChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		return &normalize.ToolChoice{Mode: mode}, nil
	}

	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("unsupported OpenAI tool_choice")
	}
	return &normalize.ToolChoice{
		Mode: choice.Type,
		Name: choice.Function.Name,
	}, nil
}

func parseOpenAIThinking(input OpenAIRequest) (normalize.ThinkingConfig, error) {
	if input.Reasoning != nil && input.Reasoning.BudgetTokens > 0 {
		return normalize.ThinkingConfig{
			Enabled:      true,
			Mode:         normalize.ThinkingModeBudget,
			BudgetTokens: input.Reasoning.BudgetTokens,
			Effort:       normalizeThinkingEffort(input.Reasoning.Effort),
		}, nil
	}

	if effort := firstNonEmpty(input.ReasoningEffort, valueOrEmpty(input.Reasoning)); effort != "" {
		switch strings.ToLower(strings.TrimSpace(effort)) {
		case "none", "off", "disabled":
			return normalize.ThinkingConfig{Enabled: false, Mode: normalize.ThinkingModeOff}, nil
		case "xhigh", "max":
			return normalize.ThinkingConfig{
				Enabled:      true,
				Mode:         normalize.ThinkingModeBudget,
				Effort:       normalize.ThinkingEffortHigh,
				BudgetTokens: normalize.DefaultBudgetForEffort(normalize.ThinkingEffortHigh) * 2,
			}, nil
		case "minimal", "low", "medium", "high":
			return normalize.ThinkingConfig{
				Enabled: true,
				Mode:    normalize.ThinkingModeEffort,
				Effort:  normalizeThinkingEffort(effort),
			}, nil
		default:
			return normalize.ThinkingConfig{}, fmt.Errorf("unsupported reasoning_effort %q", effort)
		}
	}

	return normalize.ThinkingConfig{Enabled: false, Mode: normalize.ThinkingModeOff}, nil
}

func normalizeThinkingEffort(raw string) normalize.ThinkingEffort {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "minimal":
		return normalize.ThinkingEffortMinimal
	case "low":
		return normalize.ThinkingEffortLow
	case "high":
		return normalize.ThinkingEffortHigh
	default:
		return normalize.ThinkingEffortMedium
	}
}

func valueOrEmpty(reasoning *OpenAIReasoning) string {
	if reasoning == nil {
		return ""
	}
	return reasoning.Effort
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultMaxTokens(value int) int {
	if value > 0 {
		return value
	}
	return 20000
}
