package modelmap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
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
	return LoadRegistryWithDiscovered(raw, nil)
}

func LoadRegistryWithDiscovered(raw string, discovered []string) (*Registry, error) {
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
	} else if len(discovered) > 0 {
		specs = mergeDefaultAndDiscovered(specs, discovered)
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

type DiscoverOptions struct {
	BaseURL        string
	AccessToken    string
	OrganizationID string
	HTTPClient     *http.Client
}

func DiscoverCatalog(ctx context.Context, opts DiscoverOptions) ([]string, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if strings.TrimSpace(opts.AccessToken) == "" || strings.TrimSpace(opts.OrganizationID) == "" {
		return nil, fmt.Errorf("access token and organization id are required")
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(opts.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build model discovery request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.AccessToken)+":"+strings.TrimSpace(opts.OrganizationID))
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discover models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			return nil, fmt.Errorf("discover models: unexpected status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("discover models: unexpected status %d: %s", resp.StatusCode, msg)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode model discovery response: %w", err)
	}

	out := make([]string, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
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

func mergeDefaultAndDiscovered(defaults []ModelSpec, discovered []string) []ModelSpec {
	byID := make(map[string]ModelSpec, len(defaults))
	for _, spec := range defaults {
		byID[spec.ID] = spec
	}

	seen := make(map[string]struct{}, len(discovered)+len(defaults))
	out := make([]ModelSpec, 0, len(discovered)+len(defaults))
	for _, id := range discovered {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if spec, ok := byID[id]; ok {
			out = append(out, spec)
			continue
		}
		out = append(out, genericModel(id))
	}

	for _, spec := range defaults {
		if _, ok := seen[spec.ID]; ok {
			continue
		}
		seen[spec.ID] = struct{}{}
		out = append(out, spec)
	}
	return out
}

func genericModel(id string) ModelSpec {
	family := id
	if head, _, ok := strings.Cut(id, "/"); ok && strings.TrimSpace(head) != "" {
		family = strings.TrimSpace(head)
	}
	return ModelSpec{
		ID:                     id,
		DisplayName:            id,
		Family:                 family,
		UpstreamModel:          id,
		CandidateModels:        []string{id},
		SupportsText:           true,
		SupportsImages:         true,
		SupportsTools:          true,
		SupportsThinkingEffort: true,
		SupportsThinkingBudget: true,
	}
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
