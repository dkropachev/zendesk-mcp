package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
)

func (r *Registry) registerRequestTools(server *mcp.Server) {
	server.RegisterTool(mcp.Tool{Name: "zendesk_list_requests", Description: "List requests visible from end-user perspective. Returns public data only, even when authenticated as agent.", InputSchema: objectSchema(map[string]any{"page_size": integer("Requests 1-100", 1), "after_cursor": stringProp("Cursor")}), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args pageArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		requests, meta, err := r.client.ListRequests(ctx, args.PageSize, args.AfterCursor)
		if err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"requests": requests, "meta": meta, "visibility": "public_only"})
	}})
	server.RegisterTool(mcp.Tool{Name: "zendesk_get_request", Description: "Get one request from end-user perspective; never exposes private agent notes.", InputSchema: objectSchema(map[string]any{"request_id": integer("Request id", 1)}, "request_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			RequestID int64 `json:"request_id"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if args.RequestID <= 0 {
			return nil, fmt.Errorf("request_id must be positive")
		}
		request, err := r.client.Request(ctx, args.RequestID)
		if err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"request": request, "visibility": "public_only"})
	}})
	server.RegisterTool(mcp.Tool{Name: "zendesk_list_request_comments", Description: "List public comments visible through Requests API. Private agent notes cannot appear.", InputSchema: objectSchema(map[string]any{"request_id": integer("Request id", 1), "page_size": integer("Comments 1-100", 1), "after_cursor": stringProp("Cursor")}, "request_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			RequestID   int64  `json:"request_id"`
			PageSize    int    `json:"page_size"`
			AfterCursor string `json:"after_cursor"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		comments, meta, err := r.client.RequestComments(ctx, args.RequestID, args.PageSize, args.AfterCursor)
		if err != nil {
			return nil, err
		}
		for _, comment := range comments {
			if !comment.Public {
				return nil, fmt.Errorf("Requests API returned unexpected private comment; refusing response")
			}
		}
		return jsonResult(map[string]any{"comments": comments, "meta": meta, "visibility": "public_only"})
	}})
}
