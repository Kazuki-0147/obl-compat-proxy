package normalize

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/openblocklabs/obl-compat-proxy/internal/modelmap"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentPartType string

const (
	ContentPartText             ContentPartType = "text"
	ContentPartImage            ContentPartType = "image"
	ContentPartThinking         ContentPartType = "thinking"
	ContentPartRedactedThinking ContentPartType = "redacted_thinking"
)

type ThinkingMode string

const (
	ThinkingModeOff    ThinkingMode = "off"
	ThinkingModeEffort ThinkingMode = "effort"
	ThinkingModeBudget ThinkingMode = "budget"
)

type ThinkingEffort string

const (
	ThinkingEffortMinimal ThinkingEffort = "minimal"
	ThinkingEffortLow     ThinkingEffort = "low"
	ThinkingEffortMedium  ThinkingEffort = "medium"
	ThinkingEffortHigh    ThinkingEffort = "high"
)

type ContentPart struct {
	Type         ContentPartType `json:"type"`
	Text         string          `json:"text,omitempty"`
	ImageURL     string          `json:"image_url,omitempty"`
	MediaType    string          `json:"media_type,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
}

type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type ToolChoice struct {
	Mode string `json:"mode"`
	Name string `json:"name,omitempty"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Message struct {
	Role       Role          `json:"role"`
	Name       string        `json:"name,omitempty"`
	Content    []ContentPart `json:"content,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type ThinkingConfig struct {
	Enabled      bool           `json:"enabled"`
	Mode         ThinkingMode   `json:"mode"`
	Effort       ThinkingEffort `json:"effort,omitempty"`
	BudgetTokens int            `json:"budget_tokens,omitempty"`
}

type Request struct {
	Protocol       string
	Model          modelmap.ModelSpec
	Messages       []Message
	Stream         bool
	IncludeUsage   bool
	Temperature    *float64
	TopP           *float64
	MaxTokens      int
	Tools          []ToolDefinition
	ToolChoice     *ToolChoice
	User           string
	Thinking       ThinkingConfig
	AnthropicBetas []string
}

type Usage struct {
	PromptTokens     int            `json:"prompt_tokens,omitempty"`
	CompletionTokens int            `json:"completion_tokens,omitempty"`
	TotalTokens      int            `json:"total_tokens,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

func NormalizeThinking(input ThinkingConfig, spec modelmap.ModelSpec) (ThinkingConfig, error) {
	if !input.Enabled || input.Mode == ThinkingModeOff {
		return ThinkingConfig{Enabled: false, Mode: ThinkingModeOff}, nil
	}

	switch input.Mode {
	case ThinkingModeBudget:
		if spec.SupportsThinkingBudget {
			if input.BudgetTokens <= 0 {
				input.BudgetTokens = defaultBudgetForEffort(input.Effort)
			}
			input.Enabled = true
			return input, nil
		}
		if spec.SupportsThinkingEffort {
			return ThinkingConfig{
				Enabled: true,
				Mode:    ThinkingModeEffort,
				Effort:  ThinkingEffortHigh,
			}, nil
		}
		return ThinkingConfig{}, fmt.Errorf("model %q does not support budget thinking", spec.ID)
	case ThinkingModeEffort:
		if spec.SupportsThinkingEffort {
			if input.Effort == "" {
				input.Effort = ThinkingEffortMedium
			}
			input.Enabled = true
			return input, nil
		}
		if spec.SupportsThinkingBudget {
			return ThinkingConfig{
				Enabled:      true,
				Mode:         ThinkingModeBudget,
				Effort:       input.Effort,
				BudgetTokens: defaultBudgetForEffort(input.Effort),
			}, nil
		}
		return ThinkingConfig{}, fmt.Errorf("model %q does not support effort thinking", spec.ID)
	default:
		return ThinkingConfig{}, fmt.Errorf("unsupported thinking mode %q", input.Mode)
	}
}

func DefaultBudgetForEffort(effort ThinkingEffort) int {
	return defaultBudgetForEffort(effort)
}

func ValidateImageURL(raw string, maxBytes int) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", errors.New("image URL cannot be empty")
	}
	if strings.HasPrefix(value, "data:") {
		mediaType, payload, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
		if !ok {
			return "", "", errors.New("invalid data URL")
		}
		mediaType = strings.TrimSuffix(mediaType, ";base64")
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return "", "", fmt.Errorf("decode data URL: %w", err)
		}
		if len(decoded) > maxBytes {
			return "", "", fmt.Errorf("data URL image exceeds %d bytes", maxBytes)
		}
		return value, mediaType, nil
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return "", "", errors.New("image URL must use http(s) or data URL")
	}
	return value, "", nil
}

func defaultBudgetForEffort(effort ThinkingEffort) int {
	switch effort {
	case ThinkingEffortMinimal:
		return 2048
	case ThinkingEffortLow:
		return 4096
	case ThinkingEffortMedium:
		return 8192
	case ThinkingEffortHigh:
		return 16384
	default:
		return 8192
	}
}
