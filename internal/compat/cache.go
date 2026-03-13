package compat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openblocklabs/obl-compat-proxy/internal/normalize"
)

func parseCacheControl(raw json.RawMessage) (*normalize.CacheControl, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var input struct {
		Type string `json:"type"`
		TTL  string `json:"ttl,omitempty"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("invalid cache_control object")
	}
	if strings.TrimSpace(input.Type) == "" {
		return nil, fmt.Errorf("cache_control.type is required")
	}

	return &normalize.CacheControl{
		Type: strings.TrimSpace(input.Type),
		TTL:  strings.TrimSpace(input.TTL),
	}, nil
}
