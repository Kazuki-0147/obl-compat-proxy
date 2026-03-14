package modelmap

import "testing"

func TestLoadRegistryWithDiscoveredAddsGenericModels(t *testing.T) {
	registry, err := LoadRegistryWithDiscovered("", []string{
		"anthropic/claude-opus-4.1",
		"anthropic/claude-opus-4.6",
		"openai/gpt-5.4",
	})
	if err != nil {
		t.Fatalf("LoadRegistryWithDiscovered returned error: %v", err)
	}

	opus41, ok := registry.Get("anthropic/claude-opus-4.1")
	if !ok {
		t.Fatalf("expected discovered model anthropic/claude-opus-4.1 to exist")
	}
	if opus41.UpstreamModel != "anthropic/claude-opus-4.1" {
		t.Fatalf("unexpected upstream model: %q", opus41.UpstreamModel)
	}
	if !opus41.SupportsThinkingBudget || !opus41.SupportsTools || !opus41.SupportsImages {
		t.Fatalf("generic discovered model should default to permissive capabilities: %+v", opus41)
	}

	opus46, ok := registry.Get("anthropic/claude-opus-4.6")
	if !ok {
		t.Fatalf("expected default model anthropic/claude-opus-4.6 to exist")
	}
	if len(opus46.CandidateModels) != 3 {
		t.Fatalf("expected special candidate models to be preserved, got %v", opus46.CandidateModels)
	}
}
