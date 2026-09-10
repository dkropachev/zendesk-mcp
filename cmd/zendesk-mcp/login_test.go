package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLoginInputCopyAsCurl(t *testing.T) {
	input := `curl --url 'https://example.zendesk.com/agent/tickets/12345' \
  -H 'accept: text/html' \
  -H 'referer: https://example.okta.com/' \
  -H 'user-agent: Test Browser' \
  -H 'x-unsafe: do-not-store' \
  -b 'browser_session=fake; edge_session=fake'`
	parsed, err := parseLoginInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.BaseURL != "https://example.zendesk.com" {
		t.Fatalf("BaseURL = %q", parsed.BaseURL)
	}
	if parsed.AuthMode != "browser" {
		t.Fatalf("AuthMode = %q", parsed.AuthMode)
	}
	if parsed.Cookie != "browser_session=fake; edge_session=fake" {
		t.Fatalf("Cookie = %q", parsed.Cookie)
	}
	if parsed.Headers["User-Agent"] != "Test Browser" || parsed.Headers["Referer"] != "https://example.okta.com/" {
		t.Fatalf("Headers = %#v", parsed.Headers)
	}
	if _, ok := parsed.Headers["X-Unsafe"]; ok {
		t.Fatalf("unsafe header stored: %#v", parsed.Headers)
	}
}

func TestParseLoginInputRejectsMissingCredentials(t *testing.T) {
	_, err := parseLoginInput("https://example.zendesk.com/agent/tickets/12345")
	if err == nil {
		t.Fatal("expected missing credentials error")
	}
}

func TestParseLoginInputExtractsBearerAndPrefersItOverCookie(t *testing.T) {
	input := `curl 'https://example.zendesk.com/api/v2/users/me.json' -H 'authorization: Bearer oauth-test-credential' -b 'session=fake'`
	parsed, err := parseLoginInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AuthMode != "oauth" || parsed.OAuthToken != "oauth-test-credential" {
		t.Fatalf("parsed=%+v", parsed)
	}
	if _, ok := parsed.Headers["Authorization"]; ok {
		t.Fatal("Authorization header retained in browser headers")
	}
}

func TestParseLoginInputExtractsBasicAPIToken(t *testing.T) {
	credentials := base64.StdEncoding.EncodeToString([]byte("agent@example.com/token:api-test-credential"))
	parsed, err := parseLoginInput(`curl 'https://example.zendesk.com/api/v2/users/me.json' -H 'Authorization: Basic ` + credentials + `'`)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AuthMode != "api_token" || parsed.Email != "agent@example.com" || parsed.APIToken != "api-test-credential" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseLoginInputExtractsUserAPIToken(t *testing.T) {
	parsed, err := parseLoginInput(`curl 'https://example.zendesk.com/api/v2/users/me.json' --user 'agent@example.com/token:api-test-credential'`)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AuthMode != "api_token" || parsed.Email != "agent@example.com" || parsed.APIToken != "api-test-credential" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseLoginInputRejectsUnsupportedAuthorization(t *testing.T) {
	_, err := parseLoginInput(`curl 'https://example.zendesk.com/api/v2/users/me.json' -H 'Authorization: Digest fake'`)
	if err == nil {
		t.Fatal("expected unsupported authorization error")
	}
}

func TestParseLoginInputRejectsBearerWhitespace(t *testing.T) {
	_, err := parseLoginInput("curl 'https://example.zendesk.com/api/v2/users/me.json' -H 'Authorization: Bearer two words'")
	if err == nil {
		t.Fatal("expected whitespace credential error")
	}
}

func TestWriteLoginFilesPermissionsAndContents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	paths, err := writeLoginFiles(dir, loginInput{
		BaseURL:  "https://example.zendesk.com",
		AuthMode: "browser",
		Cookie:   "session=secret",
		Headers:  map[string]string{"User-Agent": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.config, paths.cookie, paths.headers} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("%s mode = %o", path, info.Mode().Perm())
		}
	}
	data, err := os.ReadFile(paths.cookie)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "session=secret\n" {
		t.Fatalf("cookie file = %q", data)
	}
	configData, err := os.ReadFile(paths.config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "session=secret") {
		t.Fatal("browser cookie leaked into config")
	}
}

func TestWriteLoginFilesPreservesSafeSettingsAndDisablesWrite(t *testing.T) {
	dir := t.TempDir()
	existing := `{"version":1,"auth_mode":"oauth","oauth_token":"must-drop","enable_write":true,"download_root":"/safe/downloads","max_read_retries":2}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := writeLoginFiles(dir, loginInput{BaseURL: "https://example.zendesk.com", AuthMode: "browser", Cookie: "session=new", Headers: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.config)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["download_root"] != "/safe/downloads" || config["max_read_retries"] != float64(2) || config["auth_mode"] != "browser" || config["enable_write"] != false {
		t.Fatalf("config=%#v", config)
	}
	if config["credential_host"] != "example.zendesk.com" {
		t.Fatalf("credential_host=%v", config["credential_host"])
	}
	if _, ok := config["oauth_token"]; ok {
		t.Fatal("OAuth token retained during browser login")
	}
}

func TestWriteLoginFilesStoresBearerOutsideConfig(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeLoginFiles(dir, loginInput{BaseURL: "https://example.zendesk.com", AuthMode: "oauth", OAuthToken: "oauth-test-credential", Headers: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if paths.credential != paths.oauthToken {
		t.Fatalf("credential=%q", paths.credential)
	}
	token, err := os.ReadFile(paths.oauthToken)
	if err != nil {
		t.Fatal(err)
	}
	if string(token) != "oauth-test-credential\n" {
		t.Fatalf("token file=%q", token)
	}
	info, _ := os.Stat(paths.oauthToken)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	configData, err := os.ReadFile(paths.config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "oauth-test-credential") {
		t.Fatal("OAuth token leaked into config")
	}
	var config map[string]any
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if config["auth_mode"] != "oauth" || config["oauth_token_file"] != paths.oauthToken || config["enable_write"] != false {
		t.Fatalf("config=%#v", config)
	}
}

func TestWriteLoginFilesStoresAPITokenOutsideConfig(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeLoginFiles(dir, loginInput{BaseURL: "https://example.zendesk.com", AuthMode: "api_token", Email: "agent@example.com", APIToken: "api-test-credential", Headers: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if paths.credential != paths.apiToken {
		t.Fatalf("credential=%q", paths.credential)
	}
	token, err := os.ReadFile(paths.apiToken)
	if err != nil {
		t.Fatal(err)
	}
	if string(token) != "api-test-credential\n" {
		t.Fatalf("token file=%q", token)
	}
	configData, err := os.ReadFile(paths.config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "api-test-credential") {
		t.Fatal("API token leaked into config")
	}
	var config map[string]any
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if config["auth_mode"] != "api_token" || config["email"] != "agent@example.com" || config["api_token_file"] != paths.apiToken {
		t.Fatalf("config=%#v", config)
	}
}
