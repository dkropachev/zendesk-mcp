package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServerProtocol(t *testing.T) {
	server := NewServer("test-server", "1.2.3")
	server.SetInstructions("test instructions")
	server.RegisterTool(Tool{
		Name:        "echo",
		Description: "Echo input",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, raw json.RawMessage) (*CallToolResult, error) {
			return &CallToolResult{Content: []Content{{Type: "text", Text: string(raw)}}}, nil
		},
	})

	initResponse, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	if !ok || !strings.Contains(string(initResponse), `"name":"test-server"`) || !strings.Contains(string(initResponse), `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize = %s, ok=%v", initResponse, ok)
	}
	callResponse, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"value":"hi"}}}`))
	if !ok || !strings.Contains(string(callResponse), `\"value\":\"hi\"`) {
		t.Fatalf("tools/call = %s, ok=%v", callResponse, ok)
	}
	if _, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); ok {
		t.Fatal("notification returned response")
	}
}

func TestServerErrors(t *testing.T) {
	server := NewServer("test-server", "1")
	parseResponse, ok := server.HandleJSON(context.Background(), []byte(`not-json`))
	if !ok || !strings.Contains(string(parseResponse), `"code":-32700`) {
		t.Fatalf("parse response = %s", parseResponse)
	}
	methodResponse, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"missing"}`))
	if !ok || !strings.Contains(string(methodResponse), `"code":-32601`) {
		t.Fatalf("method response = %s", methodResponse)
	}
}
