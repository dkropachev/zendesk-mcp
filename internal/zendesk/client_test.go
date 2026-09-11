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
	"time"
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

	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "secret", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
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
	client, err := New(Config{BaseURL: server.URL, AuthMode: "api_token", Email: "agent@example.com", APIToken: "secret", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
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
	client, err := New(Config{BaseURL: server.URL, AuthMode: "browser", CookieFile: cookiePath, HeadersFile: headersPath, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
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
	client, err := New(Config{BaseURL: server.URL, AuthMode: "browser", Cookie: "session=old", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
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
	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "expired", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
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
	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "secret", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true, MaxResponseBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/api/v2/../admin", nil, 16); err == nil {
		t.Fatal("expected invalid path error")
	}
	if resp, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 16); err == nil || !strings.Contains(err.Error(), "RESPONSE_TOO_LARGE") || resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloadDefaultsUnlimitedAndAcceptsLargeOptionalCap(t *testing.T) {
	if got := defaultConfig().MaxDownloadBytes; got != 0 {
		t.Fatalf("default max download bytes=%d", got)
	}
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	client, err := New(Config{
		BaseURL:                       server.URL,
		AuthMode:                      "oauth",
		OAuthToken:                    "secret",
		Timeout:                       25 * time.Millisecond,
		MaxDownloadBytes:              maxZendeskUploadBytes + 1,
		TLSSkipVerify:                 true,
		AllowNonZendeskHostForTesting: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.Config().MaxDownloadBytes != maxZendeskUploadBytes+1 {
		t.Fatalf("max download bytes=%d", client.Config().MaxDownloadBytes)
	}
	if client.downloadClient.Timeout != 0 || client.storageClient.Timeout != 0 {
		t.Fatal("download clients have whole-response timeout")
	}
	for name, roundTripper := range map[string]http.RoundTripper{
		"tenant":  client.downloadClient.Transport,
		"storage": client.storageClient.Transport,
	} {
		transport, ok := roundTripper.(*http.Transport)
		if !ok {
			t.Fatalf("%s transport type=%T", name, roundTripper)
		}
		if transport.ResponseHeaderTimeout != 25*time.Millisecond {
			t.Fatalf("%s header timeout=%v", name, transport.ResponseHeaderTimeout)
		}
		if transport.TLSHandshakeTimeout != 25*time.Millisecond {
			t.Fatalf("%s TLS handshake timeout=%v", name, transport.TLSHandshakeTimeout)
		}
		if transport.DialContext == nil {
			t.Fatalf("%s transport has no bounded dialer", name)
		}
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

func TestNewRejectsNonZendeskCredentialHost(t *testing.T) {
	_, err := New(Config{BaseURL: "https://collector.example", AuthMode: "oauth", OAuthToken: "fake"})
	if err == nil || !strings.Contains(err.Error(), "zendesk.com tenant") {
		t.Fatalf("error=%v", err)
	}
}

func TestNewRejectsInvalidDNSHostnameBeforeZendeskSuffixCheck(t *testing.T) {
	for _, baseURL := range []string{
		"https://[::ffff:127.0.0.1%25x.zendesk.com]",
		"https://[::1]",
		"https://127.0.0.1",
		"https://tenant..zendesk.com",
		"https://-tenant.zendesk.com",
		"https://tenant_.zendesk.com",
		"https://ténant.zendesk.com",
		"https://vİctim.zendesk.com",
	} {
		t.Run(baseURL, func(t *testing.T) {
			_, err := New(Config{BaseURL: baseURL, AuthMode: "oauth", OAuthToken: "fake", TLSSkipVerify: true})
			if err == nil || !strings.Contains(err.Error(), "invalid base URL host") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestEnvironmentCannotRebindStoredCredentialsToAnotherTenant(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	config := `{"version":1,"base_url":"https://tenant-a.zendesk.com","auth_mode":"oauth","oauth_token_file":"/tmp/fake-token"}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZENDESK_CONFIG_FILE", configPath)
	t.Setenv("ZENDESK_BASE_URL", "https://tenant-b.zendesk.com")
	_, err := ConfigFromEnv()
	if err == nil || !strings.Contains(err.Error(), "credential host") {
		t.Fatalf("error=%v", err)
	}
}

func TestEnvironmentCredentialFileRequiresPersistentHostBinding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZENDESK_CONFIG_FILE", "")
	t.Setenv("ZENDESK_BASE_URL", "https://tenant-a.zendesk.com")
	t.Setenv("ZENDESK_AUTH_MODE", "oauth")
	t.Setenv("ZENDESK_OAUTH_TOKEN_FILE", "/tmp/fake-token")

	_, err := ConfigFromEnv()
	if err == nil || !strings.Contains(err.Error(), "persistent tenant binding") {
		t.Fatalf("error=%v", err)
	}
}

func TestEnvironmentIgnoresInactiveCredentialFileForHostBinding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZENDESK_CONFIG_FILE", "")
	t.Setenv("ZENDESK_BASE_URL", "https://tenant-a.zendesk.com")
	t.Setenv("ZENDESK_AUTH_MODE", "oauth")
	t.Setenv("ZENDESK_OAUTH_TOKEN", "inline-token")
	t.Setenv("ZENDESK_OAUTH_TOKEN_FILE", "/tmp/inactive-token")

	if _, err := ConfigFromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentInlineCredentialCanReplaceStoredTenant(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	config := `{"version":1,"base_url":"https://tenant-a.zendesk.com","credential_host":"tenant-a.zendesk.com","auth_mode":"oauth","oauth_token_file":"/tmp/tenant-a-token"}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZENDESK_CONFIG_FILE", configPath)
	t.Setenv("ZENDESK_BASE_URL", "https://tenant-b.zendesk.com")
	t.Setenv("ZENDESK_OAUTH_TOKEN", "tenant-b-inline-token")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CredentialHost != "tenant-b.zendesk.com" {
		t.Fatalf("credential host=%q", cfg.CredentialHost)
	}
}

func TestEnvironmentInlineCredentialCanReplaceCredentialFreeConfigTenant(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":1,"base_url":"https://tenant-a.zendesk.com","auth_mode":"oauth"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZENDESK_CONFIG_FILE", configPath)
	t.Setenv("ZENDESK_BASE_URL", "https://tenant-b.zendesk.com")
	t.Setenv("ZENDESK_OAUTH_TOKEN", "tenant-b-inline-token")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CredentialHost != "tenant-b.zendesk.com" {
		t.Fatalf("credential host=%q", cfg.CredentialHost)
	}
}
