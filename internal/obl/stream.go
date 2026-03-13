package obl

import (
	"encoding/json"
	"strings"

	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
)

type Chunk struct {
	ID       string        `json:"id"`
	Object   string        `json:"object"`
	Created  int64         `json:"created"`
	Model    string        `json:"model"`
	Provider string        `json:"provider,omitempty"`
	Choices  []ChunkChoice `json:"choices"`
	Usage    *ChunkUsage   `json:"usage,omitempty"`
}

type ChunkChoice struct {
	Index              int        `json:"index"`
	Delta              ChunkDelta `json:"delta"`
	FinishReason       *string    `json:"finish_reason"`
	NativeFinishReason *string    `json:"native_finish_reason,omitempty"`
}

type ChunkDelta struct {
	Role             string                 `json:"role,omitempty"`
	Content          string                 `json:"content,omitempty"`
	Reasoning        *string                `json:"reasoning,omitempty"`
	ReasoningDetails []ChunkReasoningDetail `json:"reasoning_details,omitempty"`
	ToolCalls        []ChunkToolCallPart    `json:"tool_calls,omitempty"`
}

type ChunkToolCallPart struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type ChunkReasoningDetail struct {
	Type      string `json:"type,omitempty"`
	Text      string `json:"text,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
	Format    string `json:"format,omitempty"`
	ID        string `json:"id,omitempty"`
	Index     int    `json:"index,omitempty"`
}

type ChunkUsage struct {
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	Extra            map[string]any `json:"-"`
}

func (u *ChunkUsage) UnmarshalJSON(data []byte) error {
	type alias ChunkUsage
	aux := alias{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*u = ChunkUsage(aux)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err == nil {
		delete(raw, "prompt_tokens")
		delete(raw, "completion_tokens")
		delete(raw, "total_tokens")
		if len(raw) > 0 {
			u.Extra = raw
		}
	}
	return nil
}

type Aggregate struct {
	ID           string
	Created      int64
	Model        string
	Role         string
	Content      strings.Builder
	ToolCalls    map[int]*normalize.ToolCall
	FinishReason string
	Usage        normalize.Usage
}

func (a *Aggregate) Consume(chunk Chunk) {
	if a.ID == "" {
		a.ID = chunk.ID
	}
	if a.Created == 0 {
		a.Created = chunk.Created
	}
	if a.Model == "" {
		a.Model = chunk.Model
	}
	if a.ToolCalls == nil {
		a.ToolCalls = map[int]*normalize.ToolCall{}
	}

	for _, choice := range chunk.Choices {
		if choice.Delta.Role != "" {
			a.Role = choice.Delta.Role
		}
		if choice.Delta.Content != "" {
			a.Content.WriteString(choice.Delta.Content)
		}
		for _, part := range choice.Delta.ToolCalls {
			call := a.ToolCalls[part.Index]
			if call == nil {
				call = &normalize.ToolCall{}
				a.ToolCalls[part.Index] = call
			}
			if part.ID != "" {
				call.ID = part.ID
			}
			if part.Function.Name != "" {
				call.Name = part.Function.Name
			}
			if part.Function.Arguments != "" {
				call.Arguments += part.Function.Arguments
			}
		}
		if choice.FinishReason != nil {
			a.FinishReason = *choice.FinishReason
		}
	}

	if chunk.Usage != nil {
		a.Usage = normalize.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
			Extra:            chunk.Usage.Extra,
		}
	}
}

func (a *Aggregate) OrderedToolCalls() []normalize.ToolCall {
	if len(a.ToolCalls) == 0 {
		return nil
	}
	maxIndex := -1
	for idx := range a.ToolCalls {
		if idx > maxIndex {
			maxIndex = idx
		}
	}
	out := make([]normalize.ToolCall, 0, len(a.ToolCalls))
	for idx := 0; idx <= maxIndex; idx++ {
		call, ok := a.ToolCalls[idx]
		if !ok || call == nil {
			continue
		}
		out = append(out, *call)
	}
	return out
}
