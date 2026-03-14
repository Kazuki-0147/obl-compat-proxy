package obl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openblocklabs/obl-compat-proxy/internal/config"
	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	tokens     *TokenSource
}

type Request struct {
	Model            string           `json:"model"`
	Models           []string         `json:"models,omitempty"`
	Messages         []RequestMessage `json:"messages"`
	Temperature      *float64         `json:"temperature,omitempty"`
	TopP             *float64         `json:"top_p,omitempty"`
	MaxTokens        int              `json:"max_tokens,omitempty"`
	Tools            []RequestTool    `json:"tools,omitempty"`
	ToolChoice       any              `json:"tool_choice,omitempty"`
	Stream           bool             `json:"stream"`
	StreamOptions    *StreamOptions   `json:"stream_options,omitempty"`
	User             string           `json:"user,omitempty"`
	ReasoningEffort  string           `json:"reasoning_effort,omitempty"`
	ReasoningEnabled *bool            `json:"reasoning_enabled,omitempty"`
	ReasoningBudget  int              `json:"reasoning_budget,omitempty"`
	AnthropicBetas   []string         `json:"-"`
}

type RequestMessage struct {
	Role       string            `json:"role"`
	Name       string            `json:"name,omitempty"`
	Content    []RequestPart     `json:"content,omitempty"`
	ToolCalls  []RequestToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type RequestPart struct {
	Type         string               `json:"type"`
	Text         string               `json:"text,omitempty"`
	Thinking     string               `json:"thinking,omitempty"`
	Signature    string               `json:"signature,omitempty"`
	Data         string               `json:"data,omitempty"`
	ImageURL     *RequestImageURL     `json:"image_url,omitempty"`
	CacheControl *RequestCacheControl `json:"cache_control,omitempty"`
}

type RequestImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type RequestCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type RequestTool struct {
	Type     string              `json:"type"`
	Function RequestToolFunction `json:"function"`
}

type RequestToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type RequestToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type UpstreamError struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
}

func (e *UpstreamError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("upstream returned status %d", e.StatusCode)
	}
	return e.Message
}

func NewClient(cfg config.Config) *Client {
	httpClient := &http.Client{
		Timeout: 2 * time.Minute,
	}
	return &Client{
		baseURL:    strings.TrimRight(cfg.OBLAPIBaseURL, "/"),
		httpClient: httpClient,
		tokens:     NewTokenSource(cfg, httpClient),
	}
}

func (c *Client) StreamChat(ctx context.Context, request Request) (*http.Response, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal upstream request: %w", err)
	}

	var trySend func(bool) (*http.Response, error)
	trySend = func(forceRefresh bool) (*http.Response, error) {
		if forceRefresh {
			if err := c.tokens.ForceRefresh(ctx); err != nil {
				return nil, err
			}
		}
		authHeader, err := c.tokens.AuthorizationHeader(ctx)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build upstream request: %w", err)
		}
		req.Header.Set("Authorization", authHeader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if len(request.AnthropicBetas) > 0 {
			req.Header.Set("anthropic-beta", strings.Join(request.AnthropicBetas, ","))
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("send upstream request: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized && !forceRefresh {
			resp.Body.Close()
			return trySend(true)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			return nil, parseUpstreamError(resp)
		}
		return resp, nil
	}

	return trySend(false)
}

func BuildRequest(req normalize.Request, includeUsage bool) Request {
	messages := applyDefaultPromptCaching(req)
	upstream := Request{
		Model:          req.Model.UpstreamModel,
		Models:         req.Model.CandidateList(),
		Messages:       buildMessages(messages),
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		MaxTokens:      req.MaxTokens,
		Tools:          buildTools(req.Tools),
		ToolChoice:     buildToolChoice(req.ToolChoice),
		Stream:         true,
		StreamOptions:  &StreamOptions{IncludeUsage: includeUsage},
		User:           req.User,
		AnthropicBetas: append([]string(nil), req.AnthropicBetas...),
	}

	switch req.Thinking.Mode {
	case normalize.ThinkingModeOff:
		enabled := false
		upstream.ReasoningEnabled = &enabled
	case normalize.ThinkingModeEffort:
		enabled := true
		upstream.ReasoningEnabled = &enabled
		upstream.ReasoningEffort = string(req.Thinking.Effort)
	case normalize.ThinkingModeBudget:
		enabled := true
		upstream.ReasoningEnabled = &enabled
		upstream.ReasoningBudget = req.Thinking.BudgetTokens
		if req.Thinking.Effort != "" {
			upstream.ReasoningEffort = string(req.Thinking.Effort)
		}
	}

	return upstream
}

func buildMessages(messages []normalize.Message) []RequestMessage {
	out := make([]RequestMessage, 0, len(messages))
	for _, message := range messages {
		item := RequestMessage{
			Role:       string(message.Role),
			Name:       message.Name,
			Content:    buildParts(message.Content),
			ToolCallID: message.ToolCallID,
		}
		if len(message.ToolCalls) > 0 {
			item.ToolCalls = make([]RequestToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				toolCall := RequestToolCall{
					ID:   call.ID,
					Type: "function",
				}
				toolCall.Function.Name = call.Name
				toolCall.Function.Arguments = call.Arguments
				item.ToolCalls = append(item.ToolCalls, toolCall)
			}
		}
		out = append(out, item)
	}
	return out
}

func buildParts(parts []normalize.ContentPart) []RequestPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]RequestPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case normalize.ContentPartText:
			out = append(out, RequestPart{
				Type:         "text",
				Text:         part.Text,
				CacheControl: buildCacheControl(part.CacheControl),
			})
		case normalize.ContentPartImage:
			out = append(out, RequestPart{
				Type: "image_url",
				ImageURL: &RequestImageURL{
					URL:    part.ImageURL,
					Detail: "auto",
				},
				CacheControl: buildCacheControl(part.CacheControl),
			})
		case normalize.ContentPartThinking:
			out = append(out, RequestPart{
				Type:      "thinking",
				Thinking:  part.Text,
				Signature: part.Signature,
			})
		case normalize.ContentPartRedactedThinking:
			out = append(out, RequestPart{
				Type: "redacted_thinking",
				Data: part.Data,
			})
		}
	}
	return out
}

func buildCacheControl(cache *normalize.CacheControl) *RequestCacheControl {
	if cache == nil {
		return nil
	}
	return &RequestCacheControl{
		Type: cache.Type,
		TTL:  cache.TTL,
	}
}

func buildTools(tools []normalize.ToolDefinition) []RequestTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]RequestTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, RequestTool{
			Type: "function",
			Function: RequestToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	return out
}

func buildToolChoice(choice *normalize.ToolChoice) any {
	if choice == nil {
		return nil
	}
	switch choice.Mode {
	case "", "auto", "none", "required":
		return choice.Mode
	case "function":
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": choice.Name,
			},
		}
	default:
		return choice.Mode
	}
}

func parseUpstreamError(resp *http.Response) error {
	payload, _ := io.ReadAll(resp.Body)
	message := strings.TrimSpace(string(payload))
	upstreamErr := &UpstreamError{
		StatusCode: resp.StatusCode,
		Message:    message,
	}

	var structured struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(payload, &structured); err == nil {
		if structured.Error.Message != "" {
			upstreamErr.Message = structured.Error.Message
			upstreamErr.Type = structured.Error.Type
			upstreamErr.Code = structured.Error.Code
			return upstreamErr
		}
		if structured.Message != "" {
			upstreamErr.Message = structured.Message
			upstreamErr.Type = structured.Type
			upstreamErr.Code = structured.Code
		}
	}

	if upstreamErr.Message == "" {
		upstreamErr.Message = fmt.Sprintf("upstream returned status %d", resp.StatusCode)
	}

	return upstreamErr
}
