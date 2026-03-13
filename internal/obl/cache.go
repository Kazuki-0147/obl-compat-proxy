package obl

import "github.com/openblocklabs/obl-compat-proxy/internal/normalize"

const (
	autoPromptCacheType          = "ephemeral"
	autoPromptCacheTTL           = "1h"
	maxAnthropicCacheBreakpoints = 4
)

type cacheTarget struct {
	messageIndex int
	partIndex    int
}

func applyDefaultPromptCaching(req normalize.Request) []normalize.Message {
	messages := cloneMessages(req.Messages)
	if req.Model.Family != "anthropic" || len(messages) == 0 {
		return messages
	}
	if hasExplicitCacheControl(messages) {
		return messages
	}

	targets := collectCacheTargets(messages)
	if len(targets) == 0 {
		return messages
	}
	if len(targets) > maxAnthropicCacheBreakpoints {
		targets = targets[len(targets)-maxAnthropicCacheBreakpoints:]
	}

	for _, target := range targets {
		messages[target.messageIndex].Content[target.partIndex].CacheControl = &normalize.CacheControl{
			Type: autoPromptCacheType,
			TTL:  autoPromptCacheTTL,
		}
	}
	return messages
}

func hasExplicitCacheControl(messages []normalize.Message) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if part.CacheControl != nil {
				return true
			}
		}
	}
	return false
}

func collectCacheTargets(messages []normalize.Message) []cacheTarget {
	targets := make([]cacheTarget, 0, len(messages))
	for messageIndex, message := range messages {
		lastPartIndex := -1
		for partIndex, part := range message.Content {
			switch part.Type {
			case normalize.ContentPartText, normalize.ContentPartImage:
				lastPartIndex = partIndex
			}
		}
		if lastPartIndex >= 0 {
			targets = append(targets, cacheTarget{
				messageIndex: messageIndex,
				partIndex:    lastPartIndex,
			})
		}
	}
	return targets
}

func cloneMessages(messages []normalize.Message) []normalize.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]normalize.Message, len(messages))
	for i, message := range messages {
		cloned[i] = message
		if len(message.Content) > 0 {
			cloned[i].Content = append([]normalize.ContentPart(nil), message.Content...)
		}
		if len(message.ToolCalls) > 0 {
			cloned[i].ToolCalls = append([]normalize.ToolCall(nil), message.ToolCalls...)
		}
	}
	return cloned
}
