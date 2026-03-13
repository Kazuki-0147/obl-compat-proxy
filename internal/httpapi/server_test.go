package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openblocklabs/obl-compat-proxy/internal/config"
	"github.com/openblocklabs/obl-compat-proxy/internal/modelmap"
)

func mustServerConfig(t *testing.T, upstreamURL string) config.Config {
	t.Helper()
	registry, err := modelmap.LoadRegistry("")
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return config.Config{
		ListenAddr:           ":0",
		ProxyAPIKey:          "test-key",
		OBLAPIBaseURL:        upstreamURL,
		OBLAccessToken:       "token",
		OBLOrganizationID:    "org",
		RequestBodyMaxBytes:  20 << 20,
		ImageDataURLMaxBytes: 10 << 20,
		ModelRegistry:        registry,
	}
}

func TestOpenAINonStreamProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token:org" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		if body["reasoning_effort"] != "high" {
			t.Fatalf("reasoning_effort = %v", body["reasoning_effort"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-opus-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello \"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-opus-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-opus-4.6\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9,\"prompt_tokens_details\":{\"cached_tokens\":1},\"completion_tokens_details\":{\"reasoning_tokens\":4},\"cost\":0.12}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	server, err := NewServer(mustServerConfig(t, upstream.URL))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model":"anthropic/claude-opus-4.6",
		"messages":[{"role":"user","content":"hi"}],
		"reasoning_effort":"high"
	}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["model"] != "anthropic/claude-opus-4.6" {
		t.Fatalf("model = %v", payload["model"])
	}
	choices := payload["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "hello world" {
		t.Fatalf("content = %v", message["content"])
	}
	usage := payload["usage"].(map[string]any)
	if usage["cost"] != 0.12 {
		t.Fatalf("cost = %#v", usage["cost"])
	}
	if _, ok := usage["prompt_tokens_details"]; !ok {
		t.Fatalf("prompt_tokens_details missing: %#v", usage)
	}
	if _, ok := usage["completion_tokens_details"]; !ok {
		t.Fatalf("completion_tokens_details missing: %#v", usage)
	}
}

func TestOpenAIProxyForwardsCacheControl(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		messages := body["messages"].([]any)
		content := messages[0].(map[string]any)["content"].([]any)
		part := content[0].(map[string]any)
		cacheControl := part["cache_control"].(map[string]any)
		if cacheControl["type"] != "ephemeral" || cacheControl["ttl"] != "1h" {
			t.Fatalf("unexpected cache_control: %#v", cacheControl)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_cache\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai/gpt-5.3-codex\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_cache\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai/gpt-5.3-codex\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	server, err := NewServer(mustServerConfig(t, upstream.URL))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model":"openai/gpt-5.3-codex",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}
		]}]
	}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicStreamProxyEmitsEventNames(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"step one\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"signature\":\"sig_123\"}]},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	server, err := NewServer(mustServerConfig(t, upstream.URL))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	imageData := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{
		"model":"anthropic/claude-sonnet-4.6",
		"stream":true,
		"thinking":{"type":"enabled","budget_tokens":4096},
		"messages":[{"role":"user","content":[
			{"type":"text","text":"hello"},
			{"type":"image","source":{"type":"url","url":"`+imageData+`"}}
		]}]
	}`))
	req.Header.Set("x-api-key", "test-key")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
		`"type":"thinking_delta"`,
		`"thinking":"step one"`,
		`"type":"signature_delta"`,
		`"signature":"sig_123"`,
		`"stop_reason":"end_turn"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, `"type":"thinking"`) != 1 {
		t.Fatalf("expected exactly one thinking block:\n%s", body)
	}
	if strings.Index(body, `"type":"signature_delta"`) > strings.Index(body, `"type":"text_delta"`) {
		t.Fatalf("signature_delta must appear before text_delta:\n%s", body)
	}
}

func TestAnthropicStreamPreservesBufferedTextChunkBoundaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2b\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"step one\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2b\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"A\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2b\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"B\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_2b\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-sonnet-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"signature\":\"sig_123\"}]},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	server, err := NewServer(mustServerConfig(t, upstream.URL))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{
		"model":"anthropic/claude-sonnet-4.6",
		"stream":true,
		"thinking":{"type":"enabled","budget_tokens":4096},
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("x-api-key", "test-key")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Count(body, `"type":"text_delta"`) != 2 {
		t.Fatalf("expected two text_delta events, got:\n%s", body)
	}
	if !strings.Contains(body, `"text":"A"`) || !strings.Contains(body, `"text":"B"`) {
		t.Fatalf("expected separate buffered text chunks, got:\n%s", body)
	}
	if strings.Contains(body, `"text":"AB"`) {
		t.Fatalf("buffered text was incorrectly merged:\n%s", body)
	}
}

func TestOpenAIStreamProxyPreservesReasoningFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_reason\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai/gpt-5.3-codex\",\"provider\":\"OpenAI\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"calc\",\"reasoning_details\":[{\"type\":\"reasoning.summary\",\"summary\":\"calc\"}]},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	server, err := NewServer(mustServerConfig(t, upstream.URL))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model":"openai/gpt-5.3-codex",
		"stream":true,
		"messages":[{"role":"user","content":"hi"}],
		"reasoning_effort":"high"
	}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"reasoning":"calc"`,
		`"reasoning_details":[{"summary":"calc","type":"reasoning.summary"}]`,
		`"model":"openai/gpt-5.3-codex"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"provider"`) {
		t.Fatalf("provider should be stripped:\n%s", body)
	}
}

func TestModelsEndpointRequiresAuth(t *testing.T) {
	server, err := NewServer(mustServerConfig(t, "http://example.com"))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAnthropicNonStreamUsageIncludesMappedDetails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_3\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-opus-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_3\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"anthropic/claude-opus-4.6\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":22,\"total_tokens\":33,\"prompt_tokens_details\":{\"cached_tokens\":2,\"cache_write_tokens\":5},\"completion_tokens_details\":{\"reasoning_tokens\":7,\"image_tokens\":4},\"is_byok\":false}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	server, err := NewServer(mustServerConfig(t, upstream.URL))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{
		"model":"anthropic/claude-opus-4.6",
		"max_tokens":128,
		"messages":[{"role":"user","content":"hi"}]
	}`))
	req.Header.Set("x-api-key", "test-key")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	usage := payload["usage"].(map[string]any)
	want := map[string]any{
		"input_tokens":                float64(11),
		"output_tokens":               float64(22),
		"cache_read_input_tokens":     float64(2),
		"cache_creation_input_tokens": float64(5),
		"reasoning_tokens":            float64(7),
		"output_image_tokens":         float64(4),
		"is_byok":                     false,
	}
	for key, expected := range want {
		if usage[key] != expected {
			t.Fatalf("%s = %#v, want %#v (usage=%#v)", key, usage[key], expected, usage)
		}
	}
}
