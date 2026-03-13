package config

import (
	"path/filepath"
	"testing"
)

func TestLoadAllowsRefreshTokenWithoutAccessToken(t *testing.T) {
	t.Setenv("PROXY_API_KEY", "proxy-key")
	t.Setenv("OBL_ACCESS_TOKEN", "")
	t.Setenv("OBL_REFRESH_TOKEN", "refresh-token")
	t.Setenv("OBL_ORGANIZATION_ID", "org_123")
	t.Setenv("OBL_TOKEN_REFRESH_URL", "https://api.workos.com/user_management/authenticate")
	t.Setenv("OBL_CLIENT_ID", "client_123")
	t.Setenv("OBL_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "missing.json"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OBLAccessToken != "" {
		t.Fatalf("OBLAccessToken = %q, want empty", cfg.OBLAccessToken)
	}
	if cfg.OBLRefreshToken != "refresh-token" {
		t.Fatalf("OBLRefreshToken = %q", cfg.OBLRefreshToken)
	}
}

func TestLoadRequiresRefreshURLWhenAccessTokenMissing(t *testing.T) {
	t.Setenv("PROXY_API_KEY", "proxy-key")
	t.Setenv("OBL_ACCESS_TOKEN", "")
	t.Setenv("OBL_REFRESH_TOKEN", "refresh-token")
	t.Setenv("OBL_ORGANIZATION_ID", "org_123")
	t.Setenv("OBL_TOKEN_REFRESH_URL", "")
	t.Setenv("OBL_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "missing.json"))

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	if got, want := err.Error(), "OBL_TOKEN_REFRESH_URL is required when OBL_ACCESS_TOKEN is empty"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}
