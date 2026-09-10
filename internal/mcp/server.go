package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	var req request
	if err := json.Unmarshal(data, &req); err != nil {
		return mustJSON(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &protocolError{Code: -32700, Message: "parse error"}}), true
	}
	if len(req.ID) == 0 {
		return nil, false
	}
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
		protocolVersion := "2024-11-05"
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.ProtocolVersion != "" {
			protocolVersion = params.ProtocolVersion
		}
		result := map[string]any{
			"protocolVersion": protocolVersion,
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
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &protocolError{Code: -32602, Message: "invalid tools/call params"}
		}
		for _, tool := range s.tools {
			if tool.Name != params.Name {
				continue
			}
			result, err := tool.Handler(ctx, params.Arguments)
			if err != nil {
				return nil, &protocolError{Code: -32000, Message: err.Error()}
			}
			return result, nil
		}
		return nil, &protocolError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", params.Name)}
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
