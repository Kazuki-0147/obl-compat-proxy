package obl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openblocklabs/obl-compat-proxy/internal/config"
)

type TokenSource struct {
	mu              sync.Mutex
	httpClient      *http.Client
	credentialsFile string
	refreshURL      string
	clientID        string
	clientSecret    string
	accessToken     string
	refreshToken    string
	organizationID  string
	expiry          time.Time
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func NewTokenSource(cfg config.Config, httpClient *http.Client) *TokenSource {
	return &TokenSource{
		httpClient:      httpClient,
		credentialsFile: cfg.OBLCredentialsFile,
		refreshURL:      cfg.OBLTokenRefreshURL,
		clientID:        cfg.OBLClientID,
		clientSecret:    cfg.OBLClientSecret,
		accessToken:     cfg.OBLAccessToken,
		refreshToken:    cfg.OBLRefreshToken,
		organizationID:  cfg.OBLOrganizationID,
		expiry:          cfg.OBLAccessTokenExpiry,
	}
}

func (s *TokenSource) AuthorizationHeader(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureFreshLocked(ctx); err != nil {
		return "", err
	}
	if s.accessToken == "" || s.organizationID == "" {
		return "", fmt.Errorf("missing upstream token or organization id")
	}
	return "Bearer " + s.accessToken + ":" + s.organizationID, nil
}

func (s *TokenSource) ForceRefresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *TokenSource) ensureFreshLocked(ctx context.Context) error {
	if s.accessToken != "" && (s.expiry.IsZero() || time.Until(s.expiry) > 2*time.Minute) {
		return nil
	}
	if err := s.reloadFromFileLocked(); err == nil && s.accessToken != "" && (s.expiry.IsZero() || time.Until(s.expiry) > 2*time.Minute) {
		return nil
	}
	if s.refreshURL == "" || s.refreshToken == "" {
		return nil
	}
	return s.refreshLocked(ctx)
}

func (s *TokenSource) reloadFromFileLocked() error {
	if s.credentialsFile == "" {
		return nil
	}
	body, err := os.ReadFile(s.credentialsFile)
	if err != nil {
		return err
	}
	var creds struct {
		OAuth struct {
			AccessToken    string `json:"access_token"`
			RefreshToken   string `json:"refresh_token"`
			OrganizationID string `json:"organization_id"`
			ExpiresAt      int64  `json:"expires_at"`
		} `json:"oauth"`
	}
	if err := json.Unmarshal(body, &creds); err != nil {
		return err
	}

	fileAccessToken := strings.TrimSpace(creds.OAuth.AccessToken)
	fileRefreshToken := strings.TrimSpace(creds.OAuth.RefreshToken)
	fileOrganizationID := strings.TrimSpace(creds.OAuth.OrganizationID)

	var fileExpiry time.Time
	if creds.OAuth.ExpiresAt > 0 {
		fileExpiry = time.UnixMilli(creds.OAuth.ExpiresAt)
	}

	// Only replace in-memory credentials when the file is newer, or when the
	// process is missing a value entirely. This avoids reloading a stale refresh
	// token from disk after a successful token rotation in-memory.
	useFileCredentials := s.accessToken == "" || (!fileExpiry.IsZero() && (s.expiry.IsZero() || fileExpiry.After(s.expiry)))

	if useFileCredentials {
		if fileAccessToken != "" {
			s.accessToken = fileAccessToken
		}
		if fileRefreshToken != "" {
			s.refreshToken = fileRefreshToken
		}
		if fileOrganizationID != "" {
			s.organizationID = fileOrganizationID
		}
		if !fileExpiry.IsZero() {
			s.expiry = fileExpiry
		}
		return nil
	}

	if s.refreshToken == "" && fileRefreshToken != "" {
		s.refreshToken = fileRefreshToken
	}
	if s.organizationID == "" && fileOrganizationID != "" {
		s.organizationID = fileOrganizationID
	}
	if s.accessToken == "" && fileAccessToken != "" {
		s.accessToken = fileAccessToken
	}
	if s.expiry.IsZero() && !fileExpiry.IsZero() {
		s.expiry = fileExpiry
	}
	return nil
}

func (s *TokenSource) refreshLocked(ctx context.Context) error {
	if s.refreshURL == "" || s.refreshToken == "" {
		return fmt.Errorf("token refresh is not configured")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", s.refreshToken)
	if s.clientID != "" {
		form.Set("client_id", s.clientID)
	}
	if s.clientSecret != "" {
		form.Set("client_secret", s.clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.refreshURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			return fmt.Errorf("refresh access token: unexpected status %d", resp.StatusCode)
		}
		return fmt.Errorf("refresh access token: unexpected status %d: %s", resp.StatusCode, msg)
	}

	var payload refreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode refresh response: %w", err)
	}
	if payload.AccessToken == "" {
		return fmt.Errorf("refresh access token: empty access_token")
	}

	s.accessToken = payload.AccessToken
	if payload.RefreshToken != "" {
		s.refreshToken = payload.RefreshToken
	}
	if payload.ExpiresIn > 0 {
		s.expiry = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	if err := s.persistCredentialsLocked(); err != nil {
		return fmt.Errorf("persist refreshed credentials: %w", err)
	}

	return nil
}

func (s *TokenSource) persistCredentialsLocked() error {
	if s.credentialsFile == "" {
		return nil
	}

	dir := filepath.Dir(s.credentialsFile)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	doc := map[string]any{}
	if body, err := os.ReadFile(s.credentialsFile); err == nil && len(body) > 0 {
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
	}

	oauth, _ := doc["oauth"].(map[string]any)
	if oauth == nil {
		oauth = map[string]any{}
	}
	oauth["access_token"] = s.accessToken
	if s.refreshToken != "" {
		oauth["refresh_token"] = s.refreshToken
	}
	if s.organizationID != "" {
		oauth["organization_id"] = s.organizationID
	}
	if !s.expiry.IsZero() {
		oauth["expires_at"] = s.expiry.UnixMilli()
	}
	doc["oauth"] = oauth

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.credentialsFile + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.credentialsFile)
}
