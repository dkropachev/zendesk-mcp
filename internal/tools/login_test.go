package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

func TestFullServerInitializationIncludesTenantLoginHelp(t *testing.T) {
	client, err := zendesk.New(zendesk.Config{BaseURL: "https://example.zendesk.com", AuthMode: "oauth", OAuthToken: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(client, "test")
	response, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	if !ok {
		t.Fatal("no initialize response")
	}
	text := string(response)
	for _, required := range []string{"https://example.zendesk.com/agent/home/tickets", "Copy as cURL", "zendesk-mcp login", "Ctrl-D", "Never paste"} {
		if !strings.Contains(text, required) {
			t.Fatalf("initialize missing %q: %s", required, text)
		}
	}
}

func TestSetupServerExposesOnlyLoginHelp(t *testing.T) {
	server := NewSetupServer("test")
	list, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if !ok {
		t.Fatal("no list response")
	}
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Result.Tools) != 1 || envelope.Result.Tools[0].Name != "zendesk_login_help" {
		t.Fatalf("tools=%+v", envelope.Result.Tools)
	}
	call, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"zendesk_login_help","arguments":{}}}`))
	if !ok {
		t.Fatal("no call response")
	}
	for _, required := range []string{"YOUR_SUBDOMAIN.zendesk.com", "DevTools", "Copy as cURL", "Ctrl-D", "/mcp verbose", "mode-0600"} {
		if !strings.Contains(string(call), required) {
			t.Fatalf("help missing %q: %s", required, call)
		}
	}
}
