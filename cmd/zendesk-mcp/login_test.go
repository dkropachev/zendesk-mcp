package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestParseLoginInputRejectsMissingCookie(t *testing.T) {
	_, err := parseLoginInput("https://example.zendesk.com/agent/tickets/12345")
	if err == nil {
		t.Fatal("expected missing cookie error")
	}
}

func TestWriteLoginFilesPermissionsAndContents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	paths, err := writeLoginFiles(dir, loginInput{
		BaseURL: "https://example.zendesk.com",
		Cookie:  "session=secret",
		Headers: map[string]string{"User-Agent": "test"},
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
}

func TestWriteLoginFilesPreservesSafeSettingsAndDisablesWrite(t *testing.T) {
	dir := t.TempDir()
	existing := `{"version":1,"auth_mode":"oauth","oauth_token":"must-drop","enable_write":true,"download_root":"/safe/downloads","max_read_retries":2}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := writeLoginFiles(dir, loginInput{BaseURL: "https://example.zendesk.com", Cookie: "session=new", Headers: map[string]string{}})
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
	if _, ok := config["oauth_token"]; ok {
		t.Fatal("OAuth token retained during browser login")
	}
}
