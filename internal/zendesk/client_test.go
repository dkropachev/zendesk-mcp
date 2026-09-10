package zendesk

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientOAuthRequest(t *testing.T) {
	var gotAuthorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v2/tickets/12345.json" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("include") != "users" {
			t.Errorf("include = %q", r.URL.Query().Get("include"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ticket":{"id":12345}}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "secret", TLSSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(context.Background(), "/api/v2/tickets/12345.json", url.Values{"include": {"users"}}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if string(resp.Body) != `{"ticket":{"id":12345}}` {
		t.Fatalf("body = %s", resp.Body)
	}
}

func TestClientAPITokenRequest(t *testing.T) {
	var authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "api_token", Email: "agent@example.com", APIToken: "secret", TLSSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 1024); err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("agent@example.com/token:secret"))
	if authorization != want {
		t.Fatalf("authorization=%q", authorization)
	}
}

func TestClientBrowserReadsSecretFilesEachRequest(t *testing.T) {
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, "cookie")
	headersPath := filepath.Join(dir, "headers.json")
	if err := os.WriteFile(cookiePath, []byte("session=first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headersPath, []byte(`{"Referer":"https://tenant.zendesk.com/agent/tickets/1","X-Unsafe":"blocked"}`), 0600); err != nil {
		t.Fatal(err)
	}

	var cookies []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies = append(cookies, r.Header.Get("Cookie"))
		if r.Header.Get("Referer") == "" {
			t.Error("missing Referer")
		}
		if r.Header.Get("X-Unsafe") != "" {
			t.Error("unsafe header forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "browser", CookieFile: cookiePath, HeadersFile: headersPath, TLSSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 1024); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookiePath, []byte("session=second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 1024); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cookies, ",") != "session=first,session=second" {
		t.Fatalf("cookies = %#v", cookies)
	}
}

func TestClientDetectsExpiredBrowserAuth(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>Sign in to Zendesk</html>"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "browser", Cookie: "session=old", TLSSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), "/api/v2/users/me.json", nil, 1024)
	var authErr AuthExpiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v, want AuthExpiredError", err)
	}
}

func TestClientReportsOAuthExpiryAction(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "expired", TLSSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), "/api/v2/users/me.json", nil, 1024)
	if err == nil || !strings.Contains(err.Error(), "OAuth token") || strings.Contains(err.Error(), "Copy as cURL") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRejectsPathTraversalAndOversize(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"large":"abcdefghijklmnopqrstuvwxyz"}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "secret", TLSSkipVerify: true, MaxResponseBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/api/v2/../admin", nil, 16); err == nil {
		t.Fatal("expected invalid path error")
	}
	if _, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 16); err == nil || !strings.Contains(err.Error(), "RESPONSE_TOO_LARGE") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigVersionMigrationAndFutureRejection(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacy, []byte(`{"base_url":"https://example.zendesk.com","auth_mode":"oauth","oauth_token":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigFromFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version=%d", cfg.Version)
	}
	future := filepath.Join(dir, "future.json")
	if err := os.WriteFile(future, []byte(`{"version":99,"base_url":"https://example.zendesk.com"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigFromFile(future); err == nil {
		t.Fatal("future config accepted")
	}
}
