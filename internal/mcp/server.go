package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

type Handler func(context.Context, json.RawMessage) (*CallToolResult, error)

type Server struct {
	name         string
	version      string
	instructions string
	tools        []Tool
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	Handler     Handler        `json:"-"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *protocolError  `json:"error,omitempty"`
}

type protocolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

const latestProtocolVersion = "2025-11-25"

func NewServer(name, version string) *Server {
	return &Server{name: name, version: version}
}

func (s *Server) SetInstructions(value string) { s.instructions = value }

func (s *Server) RegisterTool(tool Tool) { s.tools = append(s.tools, tool) }

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		data, ok := s.HandleJSON(ctx, scanner.Bytes())
		if !ok {
			continue
		}
		if _, err := out.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) HandleJSON(ctx context.Context, data []byte) ([]byte, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, false
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		return mustJSON(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &protocolError{Code: -32700, Message: "parse error"}}), true
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return invalidRequestResponse(json.RawMessage("null")), true
	}

	id := json.RawMessage("null")
	requestID, hasID := fields["id"]
	if hasID {
		if !validRequestID(requestID) {
			return invalidRequestResponse(id), true
		}
		id = requestID
	}

	var req request
	jsonRPC, ok := requiredString(fields, "jsonrpc")
	if !ok || jsonRPC != "2.0" {
		return invalidRequestResponse(id), true
	}
	method, ok := requiredString(fields, "method")
	if !ok {
		return invalidRequestResponse(id), true
	}
	req.JSONRPC = jsonRPC
	req.Method = method
	if raw, ok := fields["params"]; ok {
		if !isJSONObject(raw) {
			return invalidRequestResponse(id), true
		}
		req.Params = raw
	}
	if !hasID {
		return nil, false
	}
	req.ID = requestID
	result, rpcErr := s.handle(ctx, req)
	resp := response{JSONRPC: "2.0", ID: req.ID, Result: result}
	if rpcErr != nil {
		resp.Result = nil
		resp.Error = rpcErr
	}
	return mustJSON(resp), true
}

func (s *Server) handle(ctx context.Context, req request) (any, *protocolError) {
	switch req.Method {
	case "initialize":
		params, ok := decodeJSONObject(req.Params)
		if !ok {
			return nil, &protocolError{Code: -32602, Message: "invalid initialize params"}
		}
		protocolVersion, ok := requiredString(params, "protocolVersion")
		if !ok || protocolVersion == "" || !isJSONObject(params["capabilities"]) {
			return nil, &protocolError{Code: -32602, Message: "invalid initialize params"}
		}
		clientInfo, ok := decodeJSONObject(params["clientInfo"])
		if !ok {
			return nil, &protocolError{Code: -32602, Message: "invalid initialize params"}
		}
		if _, ok := requiredString(clientInfo, "name"); !ok {
			return nil, &protocolError{Code: -32602, Message: "invalid initialize params"}
		}
		if _, ok := requiredString(clientInfo, "version"); !ok {
			return nil, &protocolError{Code: -32602, Message: "invalid initialize params"}
		}
		negotiatedVersion := latestProtocolVersion
		if supportedProtocolVersions[protocolVersion] {
			negotiatedVersion = protocolVersion
		}
		result := map[string]any{
			"protocolVersion": negotiatedVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": s.name, "version": s.version},
		}
		if s.instructions != "" {
			result["instructions"] = s.instructions
		}
		return result, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		params, ok := decodeJSONObject(req.Params)
		if !ok {
			return nil, &protocolError{Code: -32602, Message: "invalid tools/call params"}
		}
		name, ok := requiredString(params, "name")
		if !ok {
			return nil, &protocolError{Code: -32602, Message: "invalid tools/call params"}
		}
		arguments := params["arguments"]
		if len(arguments) != 0 && !isJSONObject(arguments) {
			return nil, &protocolError{Code: -32602, Message: "invalid tools/call params"}
		}
		for _, tool := range s.tools {
			if tool.Name != name {
				continue
			}
			result, err := tool.Handler(ctx, arguments)
			if err != nil {
				return &CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
			}
			return result, nil
		}
		return nil, &protocolError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", name)}
	default:
		return nil, &protocolError{Code: -32601, Message: "method not found"}
	}
}

func TextResult(value string) *CallToolResult {
	return &CallToolResult{Content: []Content{{Type: "text", Text: value}}}
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func invalidRequestResponse(id json.RawMessage) []byte {
	return mustJSON(response{JSONRPC: "2.0", ID: id, Error: &protocolError{Code: -32600, Message: "invalid request"}})
}

func validRequestID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '"' {
		var value string
		return json.Unmarshal(raw, &value) == nil
	}
	if raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return false
	}
	return validJSONInteger(raw)
}

// validJSONInteger checks the mathematical value rather than requiring an
// integer's lexical form. The surrounding message has already passed
// json.Valid, so raw is known to use JSON number syntax.
func validJSONInteger(raw []byte) bool {
	unsigned := raw
	if unsigned[0] == '-' {
		unsigned = unsigned[1:]
	}
	mantissa := unsigned
	var exponent []byte
	if index := bytes.IndexAny(unsigned, "eE"); index >= 0 {
		mantissa = unsigned[:index]
		exponent = unsigned[index+1:]
	}
	fractionDigits := 0
	if decimal := bytes.IndexByte(mantissa, '.'); decimal >= 0 {
		fractionDigits = len(mantissa) - decimal - 1
	}
	allZero := true
	for _, digit := range mantissa {
		if digit != '.' && digit != '0' {
			allZero = false
			break
		}
	}
	if allZero {
		return true
	}
	trailingZeros := 0
	for index := len(mantissa) - 1; index >= 0; index-- {
		if mantissa[index] == '.' {
			continue
		}
		if mantissa[index] != '0' {
			break
		}
		trailingZeros++
	}
	return decimalExponentAtLeast(exponent, fractionDigits-trailingZeros)
}

func decimalExponentAtLeast(raw []byte, threshold int) bool {
	if len(raw) == 0 {
		return 0 >= threshold
	}
	negative := raw[0] == '-'
	if negative || raw[0] == '+' {
		raw = raw[1:]
	}
	limit := threshold
	if limit < 0 {
		limit = -limit
	}
	limit++
	magnitude := 0
	for _, digit := range raw {
		value := int(digit - '0')
		if magnitude > limit/10 || magnitude*10+value > limit {
			return !negative
		}
		magnitude = magnitude*10 + value
	}
	if negative {
		magnitude = -magnitude
	}
	return magnitude >= threshold
}

func decodeJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, false
	}
	return value, true
}

func isJSONObject(raw json.RawMessage) bool {
	_, ok := decodeJSONObject(raw)
	return ok
}

func requiredString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok {
		return "", false
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", false
	}
	return *value, true
}
