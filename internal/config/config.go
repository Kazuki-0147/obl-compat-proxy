package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openblocklabs/obl-compat-proxy/internal/modelmap"
)

const (
	defaultListenAddr        = ":8080"
	defaultOBLAPIBaseURL     = "https://dashboard.openblocklabs.com/api/v1"
	defaultCredentialsRel    = ".ob1/credentials.json"
	defaultRequestBodyMaxMB  = 20
	defaultImageDataMaxBytes = 10 << 20
)

type Config struct {
	ListenAddr           string
	ProxyAPIKey          string
	OBLAPIBaseURL        string
	OBLTokenRefreshURL   string
	OBLClientID          string
	OBLClientSecret      string
	OBLAccessToken       string
	OBLRefreshToken      string
	OBLOrganizationID    string
	OBLAccessTokenExpiry time.Time
	OBLCredentialsFile   string
	RequestBodyMaxBytes  int64
	ImageDataURLMaxBytes int
	ModelRegistry        *modelmap.Registry
}

type ob1CredentialsFile struct {
	OAuth struct {
		AccessToken          string `json:"access_token"`
		RefreshToken         string `json:"refresh_token"`
		ExpiresAtMillis      int64  `json:"expires_at"`
		OrganizationID       string `json:"organization_id"`
		AuthenticationMethod string `json:"authentication_method"`
	} `json:"oauth"`
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddr:           stringOrDefault(os.Getenv("LISTEN_ADDR"), defaultListenAddr),
		ProxyAPIKey:          strings.TrimSpace(os.Getenv("PROXY_API_KEY")),
		OBLAPIBaseURL:        strings.TrimRight(stringOrDefault(os.Getenv("OBL_API_BASE_URL"), defaultOBLAPIBaseURL), "/"),
		OBLTokenRefreshURL:   strings.TrimSpace(os.Getenv("OBL_TOKEN_REFRESH_URL")),
		OBLClientID:          strings.TrimSpace(os.Getenv("OBL_CLIENT_ID")),
		OBLClientSecret:      strings.TrimSpace(os.Getenv("OBL_CLIENT_SECRET")),
		OBLAccessToken:       strings.TrimSpace(os.Getenv("OBL_ACCESS_TOKEN")),
		OBLRefreshToken:      strings.TrimSpace(os.Getenv("OBL_REFRESH_TOKEN")),
		OBLOrganizationID:    strings.TrimSpace(os.Getenv("OBL_ORGANIZATION_ID")),
		OBLCredentialsFile:   strings.TrimSpace(os.Getenv("OBL_CREDENTIALS_FILE")),
		RequestBodyMaxBytes:  int64(intOrDefault(os.Getenv("REQUEST_BODY_MAX_MB"), defaultRequestBodyMaxMB) << 20),
		ImageDataURLMaxBytes: intOrDefault(os.Getenv("IMAGE_DATA_URL_MAX_BYTES"), defaultImageDataMaxBytes),
	}

	if cfg.OBLCredentialsFile == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			cfg.OBLCredentialsFile = filepath.Join(home, defaultCredentialsRel)
		}
	}

	if raw := strings.TrimSpace(os.Getenv("OBL_ACCESS_TOKEN_EXPIRES_AT")); raw != "" {
		expiry, err := parseExpiry(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OBL_ACCESS_TOKEN_EXPIRES_AT: %w", err)
		}
		cfg.OBLAccessTokenExpiry = expiry
	}

	if err := loadCredentialFallbacks(&cfg); err != nil {
		return Config{}, err
	}

	registry, err := modelmap.LoadRegistry(os.Getenv("MODEL_MAP_JSON"))
	if err != nil {
		return Config{}, fmt.Errorf("load model registry: %w", err)
	}
	cfg.ModelRegistry = registry

	if cfg.ProxyAPIKey == "" {
		return Config{}, errors.New("PROXY_API_KEY is required")
	}
	if cfg.OBLOrganizationID == "" {
		return Config{}, errors.New("OBL organization id is required")
	}
	if cfg.OBLAccessToken == "" {
		if cfg.OBLRefreshToken == "" {
			return Config{}, errors.New("OBL access token or refresh token is required")
		}
		if cfg.OBLTokenRefreshURL == "" {
			return Config{}, errors.New("OBL_TOKEN_REFRESH_URL is required when OBL_ACCESS_TOKEN is empty")
		}
	}

	return cfg, nil
}

func loadCredentialFallbacks(cfg *Config) error {
	if cfg.OBLAccessToken != "" && cfg.OBLOrganizationID != "" {
		return nil
	}
	if cfg.OBLCredentialsFile == "" {
		return nil
	}

	body, err := os.ReadFile(cfg.OBLCredentialsFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read credentials file: %w", err)
	}

	var creds ob1CredentialsFile
	if err := json.Unmarshal(body, &creds); err != nil {
		return fmt.Errorf("parse credentials file: %w", err)
	}

	if cfg.OBLAccessToken == "" {
		cfg.OBLAccessToken = strings.TrimSpace(creds.OAuth.AccessToken)
	}
	if cfg.OBLRefreshToken == "" {
		cfg.OBLRefreshToken = strings.TrimSpace(creds.OAuth.RefreshToken)
	}
	if cfg.OBLOrganizationID == "" {
		cfg.OBLOrganizationID = strings.TrimSpace(creds.OAuth.OrganizationID)
	}
	if cfg.OBLAccessTokenExpiry.IsZero() && creds.OAuth.ExpiresAtMillis > 0 {
		cfg.OBLAccessTokenExpiry = time.UnixMilli(creds.OAuth.ExpiresAtMillis)
	}

	return nil
}

func parseExpiry(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if millis, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if millis > 1_000_000_000_000 {
			return time.UnixMilli(millis), nil
		}
		return time.Unix(millis, 0), nil
	}
	return time.Parse(time.RFC3339, raw)
}

func stringOrDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func intOrDefault(v string, fallback int) int {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
