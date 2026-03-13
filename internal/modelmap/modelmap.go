package modelmap

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type ModelSpec struct {
	ID                     string   `json:"id"`
	DisplayName            string   `json:"display_name"`
	Family                 string   `json:"family"`
	UpstreamModel          string   `json:"upstream_model"`
	CandidateModels        []string `json:"candidate_models"`
	SupportsText           bool     `json:"supports_text"`
	SupportsImages         bool     `json:"supports_images"`
	SupportsTools          bool     `json:"supports_tools"`
	SupportsThinkingEffort bool     `json:"supports_thinking_effort"`
	SupportsThinkingBudget bool     `json:"supports_thinking_budget"`
}

type Registry struct {
	models map[string]ModelSpec
	order  []string
}

func LoadRegistry(raw string) (*Registry, error) {
	specs := defaultModels()
	if strings.TrimSpace(raw) != "" {
		var overrides []ModelSpec
		if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
			return nil, fmt.Errorf("parse MODEL_MAP_JSON: %w", err)
		}
		if len(overrides) == 0 {
			return nil, fmt.Errorf("MODEL_MAP_JSON must define at least one model")
		}
		specs = overrides
	}

	registry := &Registry{
		models: make(map[string]ModelSpec, len(specs)),
		order:  make([]string, 0, len(specs)),
	}

	for _, spec := range specs {
		if strings.TrimSpace(spec.ID) == "" {
			return nil, fmt.Errorf("model id cannot be empty")
		}
		if strings.TrimSpace(spec.UpstreamModel) == "" {
			return nil, fmt.Errorf("model %q missing upstream_model", spec.ID)
		}
		spec.ID = strings.TrimSpace(spec.ID)
		spec.UpstreamModel = strings.TrimSpace(spec.UpstreamModel)
		spec.DisplayName = strings.TrimSpace(spec.DisplayName)
		spec.Family = strings.TrimSpace(spec.Family)
		if spec.DisplayName == "" {
			spec.DisplayName = spec.ID
		}
		if len(spec.CandidateModels) == 0 {
			spec.CandidateModels = []string{spec.UpstreamModel}
		}
		registry.models[spec.ID] = spec
		registry.order = append(registry.order, spec.ID)
	}

	return registry, nil
}

func (r *Registry) Get(id string) (ModelSpec, bool) {
	spec, ok := r.models[strings.TrimSpace(id)]
	return spec, ok
}

func (r *Registry) All() []ModelSpec {
	out := make([]ModelSpec, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.models[id])
	}
	return out
}

func (m ModelSpec) SupportsImageInput() bool {
	return m.SupportsText && m.SupportsImages
}

func (m ModelSpec) CandidateList() []string {
	return slices.Clone(m.CandidateModels)
}

func defaultModels() []ModelSpec {
	return []ModelSpec{
		{
			ID:                     "anthropic/claude-opus-4.6",
			DisplayName:            "Claude Opus 4.6",
			Family:                 "anthropic",
			UpstreamModel:          "anthropic/claude-opus-4.6",
			CandidateModels:        []string{"anthropic/claude-opus-4.6", "openai/gpt-5.3-codex", "google/gemini-3.1-pro-preview"},
			SupportsText:           true,
			SupportsImages:         true,
			SupportsTools:          true,
			SupportsThinkingEffort: true,
			SupportsThinkingBudget: true,
		},
		{
			ID:                     "anthropic/claude-sonnet-4.6",
			DisplayName:            "Claude Sonnet 4.6",
			Family:                 "anthropic",
			UpstreamModel:          "anthropic/claude-sonnet-4.6",
			CandidateModels:        []string{"anthropic/claude-sonnet-4.6", "openai/gpt-5.3-codex", "google/gemini-3.1-pro-preview"},
			SupportsText:           true,
			SupportsImages:         true,
			SupportsTools:          true,
			SupportsThinkingEffort: true,
			SupportsThinkingBudget: true,
		},
		{
			ID:                     "openai/gpt-5.4",
			DisplayName:            "GPT-5.4",
			Family:                 "openai",
			UpstreamModel:          "openai/gpt-5.4",
			SupportsText:           true,
			SupportsImages:         true,
			SupportsTools:          true,
			SupportsThinkingEffort: true,
			SupportsThinkingBudget: false,
		},
		{
			ID:                     "openai/gpt-5.4-pro",
			DisplayName:            "GPT-5.4 Pro",
			Family:                 "openai",
			UpstreamModel:          "openai/gpt-5.4-pro",
			SupportsText:           true,
			SupportsImages:         true,
			SupportsTools:          true,
			SupportsThinkingEffort: true,
			SupportsThinkingBudget: false,
		},
		{
			ID:                     "openai/gpt-5.3-codex",
			DisplayName:            "GPT-5.3 Codex",
			Family:                 "openai",
			UpstreamModel:          "openai/gpt-5.3-codex",
			SupportsText:           true,
			SupportsImages:         true,
			SupportsTools:          true,
			SupportsThinkingEffort: true,
			SupportsThinkingBudget: true,
		},
		{
			ID:                     "google/gemini-3.1-pro-preview",
			DisplayName:            "Gemini 3.1 Pro Preview",
			Family:                 "google",
			UpstreamModel:          "google/gemini-3.1-pro-preview",
			SupportsText:           true,
			SupportsImages:         true,
			SupportsTools:          true,
			SupportsThinkingEffort: true,
			SupportsThinkingBudget: true,
		},
	}
}
