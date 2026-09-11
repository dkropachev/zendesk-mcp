package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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

	initResponse, ok := server.HandleJSON(context.Background(), initializeRequest("2025-06-18"))
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

func TestInitializeNegotiatesEverySupportedProtocolVersion(t *testing.T) {
	server := NewServer("test-server", "1")
	for _, version := range []string{"2024-11-05", "2025-06-18", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			data, ok := server.HandleJSON(context.Background(), initializeRequest(version))
			if !ok || !strings.Contains(string(data), `"protocolVersion":"`+version+`"`) {
				t.Fatalf("initialize=%s, ok=%v", data, ok)
			}
		})
	}
}

func TestInitializeDoesNotAdvertiseBatchProtocolWithoutBatchSupport(t *testing.T) {
	server := NewServer("test-server", "1")
	data, ok := server.HandleJSON(context.Background(), initializeRequest("2025-03-26"))
	if !ok || !strings.Contains(string(data), `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("initialize=%s, ok=%v", data, ok)
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
	invalidVersion, ok := server.HandleJSON(context.Background(), initializeRequest("unsupported"))
	if !ok || !strings.Contains(string(invalidVersion), `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("version response=%s", invalidVersion)
	}
	invalidJSONRPC, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"1.0","id":3,"method":"ping"}`))
	if !ok || !strings.Contains(string(invalidJSONRPC), `"code":-32600`) {
		t.Fatalf("jsonrpc response=%s", invalidJSONRPC)
	}
	invalidParams, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":4,"method":"initialize","params":"bad"}`))
	if !ok || !strings.Contains(string(invalidParams), `"code":-32600`) {
		t.Fatalf("params response=%s", invalidParams)
	}
}

func TestServerRejectsInvalidRequestEnvelope(t *testing.T) {
	server := NewServer("test", "1")
	tests := []struct {
		name   string
		input  string
		wantID string
	}{
		{name: "top-level array", input: `[]`, wantID: `null`},
		{name: "null jsonrpc", input: `{"jsonrpc":null,"id":1,"method":"ping"}`, wantID: `1`},
		{name: "null method", input: `{"jsonrpc":"2.0","id":1,"method":null}`, wantID: `1`},
		{name: "null notification method", input: `{"jsonrpc":"2.0","method":null}`, wantID: `null`},
		{name: "numeric method", input: `{"jsonrpc":"2.0","id":1,"method":1}`, wantID: `1`},
		{name: "boolean id", input: `{"jsonrpc":"2.0","id":true,"method":"ping"}`, wantID: `null`},
		{name: "object id", input: `{"jsonrpc":"2.0","id":{},"method":"ping"}`, wantID: `null`},
		{name: "null id", input: `{"jsonrpc":"2.0","id":null,"method":"ping"}`, wantID: `null`},
		{name: "fractional id", input: `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, wantID: `null`},
		{name: "tiny fractional id", input: `{"jsonrpc":"2.0","id":1e-999999999999999999999,"method":"ping"}`, wantID: `null`},
		{name: "scalar params", input: `{"jsonrpc":"2.0","id":"request-id","method":"ping","params":true}`, wantID: `"request-id"`},
		{name: "array params", input: `{"jsonrpc":"2.0","id":2,"method":"ping","params":[]}`, wantID: `2`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, ok := server.HandleJSON(context.Background(), []byte(test.input))
			if !ok {
				t.Fatal("invalid request returned no response")
			}
			var response struct {
				ID    json.RawMessage `json:"id"`
				Error *protocolError  `json:"error"`
			}
			if err := json.Unmarshal(data, &response); err != nil {
				t.Fatal(err)
			}
			if response.Error == nil || response.Error.Code != -32600 {
				t.Fatalf("response=%s, want invalid request", data)
			}
			if string(response.ID) != test.wantID {
				t.Fatalf("id=%s, want %s", response.ID, test.wantID)
			}
		})
	}
}

func TestServerRejectsInvalidUTF8WithoutEchoingIt(t *testing.T) {
	input := append([]byte(`{"jsonrpc":"2.0","id":"`), 0xff)
	input = append(input, []byte(`","method":"ping"}`)...)
	data, ok := NewServer("test", "1").HandleJSON(context.Background(), input)
	if !ok || !utf8.Valid(data) || !strings.Contains(string(data), `"code":-32700`) {
		t.Fatalf("response=%q, ok=%v", data, ok)
	}
}

func TestServerAcceptsMathematicalIntegerRequestIDs(t *testing.T) {
	server := NewServer("test", "1")
	for _, id := range []string{"1.0", "1e3", "100e-2", "1e999999999999999999999", "0e-999999999999999999999"} {
		t.Run(id, func(t *testing.T) {
			request := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"ping"}`, id)
			data, ok := server.HandleJSON(context.Background(), []byte(request))
			if !ok {
				t.Fatal("request returned no response")
			}
			var response struct {
				ID    json.RawMessage `json:"id"`
				Error *protocolError  `json:"error"`
			}
			if err := json.Unmarshal(data, &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != nil || string(response.ID) != id {
				t.Fatalf("response=%s, want id=%s", data, id)
			}
		})
	}
}

func TestInitializeRequiresSchemaFields(t *testing.T) {
	server := NewServer("test", "1")
	tests := []struct {
		name   string
		params string
	}{
		{name: "missing protocol version", params: `{"capabilities":{},"clientInfo":{"name":"client","version":"1"}}`},
		{name: "null protocol version", params: `{"protocolVersion":null,"capabilities":{},"clientInfo":{"name":"client","version":"1"}}`},
		{name: "wrong protocol version type", params: `{"protocolVersion":1,"capabilities":{},"clientInfo":{"name":"client","version":"1"}}`},
		{name: "missing capabilities", params: `{"protocolVersion":"2025-06-18","clientInfo":{"name":"client","version":"1"}}`},
		{name: "null capabilities", params: `{"protocolVersion":"2025-06-18","capabilities":null,"clientInfo":{"name":"client","version":"1"}}`},
		{name: "missing client info", params: `{"protocolVersion":"2025-06-18","capabilities":{}}`},
		{name: "missing client name", params: `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"version":"1"}}`},
		{name: "null client name", params: `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":null,"version":"1"}}`},
		{name: "missing client version", params: `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"client"}}`},
		{name: "null client version", params: `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"client","version":null}}`},
		{name: "wrong client version type", params: `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"client","version":1}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":%s}`, test.params)
			data, ok := server.HandleJSON(context.Background(), []byte(request))
			if !ok || !strings.Contains(string(data), `"code":-32602`) {
				t.Fatalf("response=%s, ok=%v", data, ok)
			}
		})
	}
}

func TestToolsCallRejectsMalformedStructureBeforeHandler(t *testing.T) {
	server := NewServer("test", "1")
	called := 0
	server.RegisterTool(Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}, Handler: func(context.Context, json.RawMessage) (*CallToolResult, error) {
		called++
		return TextResult("called"), nil
	}})
	tests := []struct {
		name   string
		params string
	}{
		{name: "missing name", params: `{"arguments":{}}`},
		{name: "null name", params: `{"name":null,"arguments":{}}`},
		{name: "numeric name", params: `{"name":1,"arguments":{}}`},
		{name: "string arguments", params: `{"name":"echo","arguments":"bad"}`},
		{name: "array arguments", params: `{"name":"echo","arguments":[]}`},
		{name: "null arguments", params: `{"name":"echo","arguments":null}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, test.params)
			data, ok := server.HandleJSON(context.Background(), []byte(request))
			if !ok || !strings.Contains(string(data), `"code":-32602`) || !strings.Contains(string(data), "invalid tools/call params") || strings.Contains(string(data), `"isError"`) {
				t.Fatalf("response=%s, ok=%v", data, ok)
			}
		})
	}
	if called != 0 {
		t.Fatalf("handler called %d times", called)
	}

	data, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`))
	if !ok || !strings.Contains(string(data), `"text":"called"`) || called != 1 {
		t.Fatalf("omitted arguments response=%s, ok=%v, called=%d", data, ok, called)
	}
}

func TestToolHandlerErrorUsesToolResult(t *testing.T) {
	server := NewServer("test", "1")
	server.RegisterTool(Tool{Name: "fails", InputSchema: map[string]any{"type": "object"}, Handler: func(context.Context, json.RawMessage) (*CallToolResult, error) {
		return nil, fmt.Errorf("actionable failure")
	}})
	response, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fails","arguments":{}}}`))
	if !ok || !strings.Contains(string(response), `"isError":true`) || !strings.Contains(string(response), "actionable failure") || strings.Contains(string(response), `"error":`) {
		t.Fatalf("response=%s", response)
	}
}

func initializeRequest(protocolVersion string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`, protocolVersion))
}
