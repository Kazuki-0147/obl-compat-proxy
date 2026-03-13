package compat

import (
	"strings"

	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
)

func BuildOpenAIUsage(usage normalize.Usage) map[string]any {
	out := map[string]any{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}
	for key, value := range usage.Extra {
		out[key] = value
	}
	return out
}

func BuildAnthropicUsage(usage normalize.Usage) map[string]any {
	out := map[string]any{
		"input_tokens":  usage.PromptTokens,
		"output_tokens": usage.CompletionTokens,
	}
	if len(usage.Extra) == 0 {
		return out
	}

	if value, ok := usage.Extra["cache_creation_input_tokens"]; ok {
		if n, ok := numberValue(value); ok {
			out["cache_creation_input_tokens"] = n
		}
	}
	if value, ok := usage.Extra["cache_read_input_tokens"]; ok {
		if n, ok := numberValue(value); ok {
			out["cache_read_input_tokens"] = n
		}
	}

	promptDetails := nestedMap(usage.Extra, "prompt_tokens_details")
	if n, ok := numberValue(promptDetails["cache_write_tokens"]); ok {
		out["cache_creation_input_tokens"] = n
	}
	if n, ok := numberValue(promptDetails["cached_tokens"]); ok {
		out["cache_read_input_tokens"] = n
	}
	if n, ok := numberValue(promptDetails["audio_tokens"]); ok {
		out["input_audio_tokens"] = n
	}
	if n, ok := numberValue(promptDetails["video_tokens"]); ok {
		out["input_video_tokens"] = n
	}
	if n, ok := numberValue(promptDetails["image_tokens"]); ok {
		out["input_image_tokens"] = n
	}

	completionDetails := nestedMap(usage.Extra, "completion_tokens_details")
	if n, ok := numberValue(completionDetails["reasoning_tokens"]); ok {
		out["reasoning_tokens"] = n
	}
	if n, ok := numberValue(completionDetails["audio_tokens"]); ok {
		out["output_audio_tokens"] = n
	}
	if n, ok := numberValue(completionDetails["image_tokens"]); ok {
		out["output_image_tokens"] = n
	}
	if n, ok := numberValue(completionDetails["accepted_prediction_tokens"]); ok {
		out["accepted_prediction_tokens"] = n
	}
	if n, ok := numberValue(completionDetails["rejected_prediction_tokens"]); ok {
		out["rejected_prediction_tokens"] = n
	}

	for key, value := range usage.Extra {
		if _, exists := out[key]; exists {
			continue
		}
		if strings.HasSuffix(key, "_details") {
			continue
		}
		out[key] = value
	}

	return out
}

func nestedMap(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	if typed, ok := raw.(map[string]any); ok {
		return typed
	}
	return nil
}

func numberValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case uint:
		return int(value), true
	case uint8:
		return int(value), true
	case uint16:
		return int(value), true
	case uint32:
		return int(value), true
	case uint64:
		return int(value), true
	case float32:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}
