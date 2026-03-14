package obl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestReloadFromFileDoesNotOverwriteNewerInMemoryTokens(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	credentialsFile := filepath.Join(dir, "credentials.json")
	fileExpiry := time.Now().Add(30 * time.Minute)
	body := []byte(`{"oauth":{"access_token":"file-access","refresh_token":"file-refresh","organization_id":"file-org","expires_at":` + jsonInt(fileExpiry.UnixMilli()) + `}}`)
	if err := os.WriteFile(credentialsFile, body, 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}

	source := &TokenSource{
		credentialsFile: credentialsFile,
		accessToken:     "memory-access",
		refreshToken:    "memory-refresh",
		organizationID:  "memory-org",
		expiry:          time.Now().Add(2 * time.Hour),
	}

	if err := source.reloadFromFileLocked(); err != nil {
		t.Fatalf("reloadFromFileLocked: %v", err)
	}

	if source.accessToken != "memory-access" {
		t.Fatalf("access token overwritten: got %q", source.accessToken)
	}
	if source.refreshToken != "memory-refresh" {
		t.Fatalf("refresh token overwritten: got %q", source.refreshToken)
	}
	if source.organizationID != "memory-org" {
		t.Fatalf("organization id overwritten: got %q", source.organizationID)
	}
}

func TestForceRefreshPersistsRotatedCredentials(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	credentialsFile := filepath.Join(dir, "credentials.json")
	initial := []byte(`{"oauth":{"access_token":"old-access","refresh_token":"old-refresh","organization_id":"org-1","expires_at":1,"authentication_method":"GoogleOAuth"}}`)
	if err := os.WriteFile(credentialsFile, initial, 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("unexpected grant_type: %q", got)
		}
		if got := r.Form.Get("refresh_token"); got != "old-refresh" {
			t.Fatalf("unexpected refresh_token: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer server.Close()

	source := &TokenSource{
		httpClient:      server.Client(),
		credentialsFile: credentialsFile,
		refreshURL:      server.URL,
		clientID:        "client-id",
		refreshToken:    "old-refresh",
		organizationID:  "org-1",
	}

	if err := source.ForceRefresh(context.Background()); err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}

	if source.accessToken != "new-access" {
		t.Fatalf("unexpected access token: %q", source.accessToken)
	}
	if source.refreshToken != "new-refresh" {
		t.Fatalf("unexpected refresh token: %q", source.refreshToken)
	}

	body, err := os.ReadFile(credentialsFile)
	if err != nil {
		t.Fatalf("read credentials file: %v", err)
	}
	var doc struct {
		OAuth struct {
			AccessToken          string `json:"access_token"`
			RefreshToken         string `json:"refresh_token"`
			OrganizationID       string `json:"organization_id"`
			AuthenticationMethod string `json:"authentication_method"`
		} `json:"oauth"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if doc.OAuth.AccessToken != "new-access" {
		t.Fatalf("persisted access token mismatch: %q", doc.OAuth.AccessToken)
	}
	if doc.OAuth.RefreshToken != "new-refresh" {
		t.Fatalf("persisted refresh token mismatch: %q", doc.OAuth.RefreshToken)
	}
	if doc.OAuth.OrganizationID != "org-1" {
		t.Fatalf("persisted org mismatch: %q", doc.OAuth.OrganizationID)
	}
	if doc.OAuth.AuthenticationMethod != "GoogleOAuth" {
		t.Fatalf("authentication_method lost: %q", doc.OAuth.AuthenticationMethod)
	}
}

func jsonInt(v int64) string {
	return strconv.FormatInt(v, 10)
}
