package compat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openblocklabs/obl-compat-proxy/internal/modelmap"
	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
	"github.com/openblocklabs/obl-compat-proxy/internal/obl"
)

type AnthropicRequest struct {
	Model       string             `json:"model"`
	System      json.RawMessage    `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	ToolChoice  json.RawMessage    `json:"tool_choice,omitempty"`
	Metadata    map[string]any     `json:"metadata,omitempty"`
	Thinking    json.RawMessage    `json:"thinking,omitempty"`
}

type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type AnthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`
}

func ParseAnthropicRequest(body []byte, registry *modelmap.Registry, imageMaxBytes int) (normalize.Request, error) {
	var input AnthropicRequest
	if err := json.Unmarshal(body, &input); err != nil {
		return normalize.Request{}, fmt.Errorf("decode Anthropic request: %w", err)
	}

	spec, ok := registry.Get(input.Model)
	if !ok {
		return normalize.Request{}, fmt.Errorf("unsupported model %q", input.Model)
	}

	messages := make([]normalize.Message, 0, len(input.Messages)+1)
	systemParts, err := parseAnthropicSystem(input.System)
	if err != nil {
		return normalize.Request{}, err
	}
	if len(systemParts) > 0 {
		messages = append(messages, normalize.Message{
			Role:    normalize.RoleSystem,
			Content: systemParts,
		})
	}

	for _, message := range input.Messages {
		items, err := parseAnthropicMessage(message, imageMaxBytes)
		if err != nil {
			return normalize.Request{}, err
		}
		messages = append(messages, items...)
	}

	tools := make([]normalize.ToolDefinition, 0, len(input.Tools))
	for _, tool := range input.Tools {
		tools = append(tools, normalize.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.InputSchema,
		})
	}

	toolChoice, err := parseAnthropicToolChoice(input.ToolChoice)
	if err != nil {
		return normalize.Request{}, err
	}

	thinking, err := parseAnthropicThinking(input.Thinking)
	if err != nil {
		return normalize.Request{}, err
	}
	thinking, err = normalize.NormalizeThinking(thinking, spec)
	if err != nil {
		return normalize.Request{}, err
	}

	return normalize.Request{
		Protocol:     "anthropic",
		Model:        spec,
		Messages:     messages,
		Stream:       input.Stream,
		IncludeUsage: true,
		Temperature:  input.Temperature,
		TopP:         input.TopP,
		MaxTokens:    defaultMaxTokens(input.MaxTokens),
		Tools:        tools,
		ToolChoice:   toolChoice,
		Thinking:     thinking,
	}, nil
}

func BuildAnthropicResponse(aggregate obl.Aggregate, requestedModel string) map[string]any {
	content := make([]map[string]any, 0, 2)
	if aggregate.Reasoning.String() != "" && aggregate.ReasoningSignature != "" {
		content = append(content, map[string]any{
			"type":      "thinking",
			"thinking":  aggregate.Reasoning.String(),
			"signature": aggregate.ReasoningSignature,
		})
	} else if aggregate.RedactedThinking != "" {
		content = append(content, map[string]any{
			"type": "redacted_thinking",
			"data": aggregate.RedactedThinking,
		})
	}
	if text := aggregate.Content.String(); text != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": text,
		})
	}
	for _, call := range aggregate.OrderedToolCalls() {
		var input any
		if call.Arguments != "" {
			_ = json.Unmarshal([]byte(call.Arguments), &input)
		}
		if input == nil {
			input = map[string]any{}
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Name,
			"input": input,
		})
	}

	return map[string]any{
		"id":            anthropicMessageID(aggregate.ID),
		"type":          "message",
		"role":          "assistant",
		"model":         requestedModel,
		"content":       content,
		"stop_reason":   AnthropicStopReason(aggregate.FinishReason),
		"stop_sequence": nil,
		"usage":         BuildAnthropicUsage(aggregate.Usage),
	}
}

func BuildAnthropicMessageStart(aggregate obl.Aggregate, requestedModel string) map[string]any {
	return map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            anthropicMessageID(aggregate.ID),
			"type":          "message",
			"role":          "assistant",
			"model":         requestedModel,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         BuildAnthropicUsage(normalize.Usage{}),
		},
	}
}

func parseAnthropicSystem(raw json.RawMessage) ([]normalize.ContentPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []normalize.ContentPart{{Type: normalize.ContentPartText, Text: text}}, nil
	}
	return parseAnthropicTextBlocks(raw)
}

func parseAnthropicMessage(message AnthropicMessage, imageMaxBytes int) ([]normalize.Message, error) {
	role := normalize.Role(strings.TrimSpace(message.Role))
	blocks, err := parseAnthropicBlocks(message.Content)
	if err != nil {
		return nil, fmt.Errorf("parse Anthropic %s content: %w", message.Role, err)
	}

	out := make([]normalize.Message, 0, 2)
	current := normalize.Message{Role: role}
	flushCurrent := func() {
		if len(current.Content) > 0 || len(current.ToolCalls) > 0 {
			out = append(out, current)
			current = normalize.Message{Role: role}
		}
	}

	for _, block := range blocks {
		switch block.blockType {
		case "text":
			current.Content = append(current.Content, normalize.ContentPart{
				Type:         normalize.ContentPartText,
				Text:         block.text,
				CacheControl: block.cacheControl,
			})
		case "thinking":
			if role != normalize.RoleAssistant {
				return nil, fmt.Errorf("Anthropic %s content cannot contain %q blocks", message.Role, block.blockType)
			}
			current.Content = append(current.Content, normalize.ContentPart{
				Type:      normalize.ContentPartThinking,
				Text:      block.text,
				Signature: block.signature,
			})
		case "redacted_thinking":
			if role != normalize.RoleAssistant {
				return nil, fmt.Errorf("Anthropic %s content cannot contain %q blocks", message.Role, block.blockType)
			}
			current.Content = append(current.Content, normalize.ContentPart{
				Type: normalize.ContentPartRedactedThinking,
				Data: block.data,
			})
		case "image":
			imageURL, mediaType, err := parseAnthropicImage(block, imageMaxBytes)
			if err != nil {
				return nil, err
			}
			current.Content = append(current.Content, normalize.ContentPart{
				Type:         normalize.ContentPartImage,
				ImageURL:     imageURL,
				MediaType:    mediaType,
				CacheControl: block.cacheControl,
			})
		case "tool_use":
			current.ToolCalls = append(current.ToolCalls, normalize.ToolCall{
				ID:        block.id,
				Name:      block.name,
				Arguments: block.arguments,
			})
		case "tool_result":
			flushCurrent()
			toolMessage := normalize.Message{
				Role:       normalize.RoleTool,
				ToolCallID: block.toolUseID,
				Content: []normalize.ContentPart{{
					Type:         normalize.ContentPartText,
					Text:         block.text,
					CacheControl: block.cacheControl,
				}},
			}
			out = append(out, toolMessage)
		default:
			return nil, fmt.Errorf("unsupported Anthropic block type %q", block.blockType)
		}
	}

	flushCurrent()

	return out, nil
}

type anthropicBlock struct {
	blockType    string
	text         string
	signature    string
	data         string
	id           string
	name         string
	toolUseID    string
	arguments    string
	cacheControl *normalize.CacheControl
	source       struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	}
}

func parseAnthropicBlocks(raw json.RawMessage) ([]anthropicBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []anthropicBlock{{blockType: "text", text: text}}, nil
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("expected string or content array")
	}

	out := make([]anthropicBlock, 0, len(items))
	for _, item := range items {
		var blockType string
		if err := json.Unmarshal(item["type"], &blockType); err != nil {
			return nil, fmt.Errorf("content block missing type")
		}
		block := anthropicBlock{blockType: blockType}
		cacheControl, err := parseCacheControl(item["cache_control"])
		if err != nil {
			return nil, err
		}
		block.cacheControl = cacheControl
		switch blockType {
		case "text":
			if err := json.Unmarshal(item["text"], &block.text); err != nil {
				return nil, fmt.Errorf("text block missing text")
			}
		case "thinking":
			if err := json.Unmarshal(item["thinking"], &block.text); err != nil {
				return nil, fmt.Errorf("thinking block missing thinking")
			}
			_ = json.Unmarshal(item["signature"], &block.signature)
		case "redacted_thinking":
			if err := json.Unmarshal(item["data"], &block.data); err != nil {
				return nil, fmt.Errorf("redacted_thinking block missing data")
			}
		case "image":
			if err := json.Unmarshal(item["source"], &block.source); err != nil {
				return nil, fmt.Errorf("image block missing source")
			}
		case "tool_use":
			var input any
			_ = json.Unmarshal(item["input"], &input)
			payload, _ := json.Marshal(input)
			block.arguments = string(payload)
			_ = json.Unmarshal(item["id"], &block.id)
			_ = json.Unmarshal(item["name"], &block.name)
		case "tool_result":
			_ = json.Unmarshal(item["tool_use_id"], &block.toolUseID)
			textValue, err := flattenAnthropicToolResult(item["content"])
			if err != nil {
				return nil, err
			}
			block.text = textValue
		default:
			return nil, fmt.Errorf("unsupported Anthropic block type %q", blockType)
		}
		out = append(out, block)
	}

	return out, nil
}

func parseAnthropicTextBlocks(raw json.RawMessage) ([]normalize.ContentPart, error) {
	blocks, err := parseAnthropicBlocks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]normalize.ContentPart, 0, len(blocks))
	for _, block := range blocks {
		if block.blockType != "text" {
			return nil, fmt.Errorf("system only supports text blocks")
		}
		out = append(out, normalize.ContentPart{
			Type:         normalize.ContentPartText,
			Text:         block.text,
			CacheControl: block.cacheControl,
		})
	}
	return out, nil
}

func flattenAnthropicToolResult(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	parts, err := parseAnthropicTextBlocks(raw)
	if err != nil {
		return "", fmt.Errorf("tool_result content: %w", err)
	}
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		values = append(values, part.Text)
	}
	return strings.Join(values, "\n"), nil
}

func parseAnthropicImage(block anthropicBlock, imageMaxBytes int) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(block.source.Type)) {
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(block.source.Data)
		if err != nil {
			return "", "", fmt.Errorf("decode Anthropic base64 image: %w", err)
		}
		if len(decoded) > imageMaxBytes {
			return "", "", fmt.Errorf("image exceeds %d bytes", imageMaxBytes)
		}
		encoded := base64.StdEncoding.EncodeToString(decoded)
		mediaType := strings.TrimSpace(block.source.MediaType)
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		return "data:" + mediaType + ";base64," + encoded, mediaType, nil
	case "url":
		return normalize.ValidateImageURL(block.source.URL, imageMaxBytes)
	default:
		return "", "", fmt.Errorf("unsupported image source type %q", block.source.Type)
	}
}

func parseAnthropicToolChoice(raw json.RawMessage) (*normalize.ToolChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var choice map[string]any
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("invalid Anthropic tool_choice")
	}
	if toolType, ok := choice["type"].(string); ok {
		switch toolType {
		case "auto", "any":
			return &normalize.ToolChoice{Mode: "auto"}, nil
		case "none":
			return &normalize.ToolChoice{Mode: "none"}, nil
		case "tool":
			name, _ := choice["name"].(string)
			return &normalize.ToolChoice{Mode: "function", Name: name}, nil
		}
	}
	return nil, fmt.Errorf("unsupported Anthropic tool_choice")
}

func parseAnthropicThinking(raw json.RawMessage) (normalize.ThinkingConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return normalize.ThinkingConfig{Enabled: false, Mode: normalize.ThinkingModeOff}, nil
	}

	var input struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
		Effort       string `json:"effort"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return normalize.ThinkingConfig{}, fmt.Errorf("invalid Anthropic thinking object")
	}

	switch strings.ToLower(strings.TrimSpace(input.Type)) {
	case "", "disabled", "off":
		return normalize.ThinkingConfig{Enabled: false, Mode: normalize.ThinkingModeOff}, nil
	case "enabled":
		if input.BudgetTokens > 0 {
			return normalize.ThinkingConfig{
				Enabled:      true,
				Mode:         normalize.ThinkingModeBudget,
				BudgetTokens: min(input.BudgetTokens, 32768),
				Effort:       normalizeThinkingEffort(input.Effort),
			}, nil
		}
		return normalize.ThinkingConfig{
			Enabled: true,
			Mode:    normalize.ThinkingModeEffort,
			Effort:  defaultAnthropicEffort(input.Effort),
		}, nil
	case "adaptive":
		if input.BudgetTokens > 0 {
			return normalize.ThinkingConfig{
				Enabled:      true,
				Mode:         normalize.ThinkingModeBudget,
				BudgetTokens: min(input.BudgetTokens, 32768),
				Effort:       normalize.ThinkingEffortMedium,
			}, nil
		}
		return normalize.ThinkingConfig{
			Enabled: true,
			Mode:    normalize.ThinkingModeEffort,
			Effort:  normalize.ThinkingEffortMedium,
		}, nil
	default:
		return normalize.ThinkingConfig{}, fmt.Errorf("unsupported Anthropic thinking type %q", input.Type)
	}
}

func anthropicMessageID(id string) string {
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	return "msg_" + id
}

func AnthropicStopReason(finish string) string {
	switch finish {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func defaultAnthropicEffort(raw string) normalize.ThinkingEffort {
	if strings.TrimSpace(raw) == "" {
		return normalize.ThinkingEffortMedium
	}
	return normalizeThinkingEffort(raw)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
