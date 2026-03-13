package obl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	if strings.TrimSpace(creds.OAuth.AccessToken) != "" {
		s.accessToken = strings.TrimSpace(creds.OAuth.AccessToken)
	}
	if strings.TrimSpace(creds.OAuth.RefreshToken) != "" {
		s.refreshToken = strings.TrimSpace(creds.OAuth.RefreshToken)
	}
	if strings.TrimSpace(creds.OAuth.OrganizationID) != "" {
		s.organizationID = strings.TrimSpace(creds.OAuth.OrganizationID)
	}
	if creds.OAuth.ExpiresAt > 0 {
		s.expiry = time.UnixMilli(creds.OAuth.ExpiresAt)
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
		return fmt.Errorf("refresh access token: unexpected status %d", resp.StatusCode)
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

	return nil
}
