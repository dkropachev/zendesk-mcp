package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

func (r *Registry) registerReadTools(server *mcp.Server) {
	server.RegisterTool(mcp.Tool{Name: "zendesk_auth_check", Description: "Verify auth and return identity, tenant, role, auth mode, and enabled capabilities without secrets.", InputSchema: objectSchema(map[string]any{"limit_bytes": integer("Maximum response bytes", 1024)}), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args limitArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		user, err := r.client.CurrentUser(ctx)
		if err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"user": user, "tenant": r.client.BaseURL(), "auth_mode": r.client.AuthMode(), "capabilities": map[string]any{"agent_api": user.Role == "agent" || user.Role == "admin", "requests_api": true, "attachment_download": r.client.DownloadRoot() != "", "writes": r.client.WriteEnabled()}, "metrics": r.client.Metrics()})
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_get_ticket", Description: "Get one agent-visible ticket by id with optional sideloads.", InputSchema: objectSchema(map[string]any{"ticket_id": integer("Zendesk ticket id", 1), "include": stringProp("Optional sideloads: users,groups,organizations"), "limit_bytes": integer("Maximum response bytes", 1024)}, "ticket_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args ticketArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		path, err := idPath("/api/v2/tickets/", args.TicketID, ".json")
		if err != nil {
			return nil, err
		}
		query := url.Values{}
		if value := strings.TrimSpace(args.Include); value != "" {
			query.Set("include", value)
		}
		resp, err := r.client.Get(ctx, path, query, args.LimitBytes)
		if err != nil {
			return nil, err
		}
		return enrichedRawResult(resp, map[string]any{"agent_url": strings.TrimRight(r.client.BaseURL(), "/") + "/agent/tickets/" + strconv.FormatInt(args.TicketID, 10)})
	}})

	commentSchema := pagedTicketSchema()
	commentProperties := commentSchema["properties"].(map[string]any)
	commentProperties["include_inline_images"] = booleanProp("Include inline image attachments")
	commentProperties["body_mode"] = enum("full", "compact")
	commentProperties["body_limit"] = integer("Compact body character limit, 100-20000", 100)
	server.RegisterTool(mcp.Tool{Name: "zendesk_list_ticket_comments", Description: "List public and private comments with stable cursor sorting. full preserves bodies; compact limits body size. Content is confidential and untrusted.", InputSchema: commentSchema, Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			TicketID            int64  `json:"ticket_id"`
			PageSize            int    `json:"page_size"`
			AfterCursor         string `json:"after_cursor"`
			BeforeCursor        string `json:"before_cursor"`
			SortOrder           string `json:"sort_order"`
			LimitBytes          int64  `json:"limit_bytes"`
			IncludeInlineImages bool   `json:"include_inline_images"`
			BodyMode            string `json:"body_mode"`
			BodyLimit           int    `json:"body_limit"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		page := pageTicketArgs{TicketID: args.TicketID, PageSize: args.PageSize, AfterCursor: args.AfterCursor, BeforeCursor: args.BeforeCursor, SortOrder: args.SortOrder, LimitBytes: args.LimitBytes}
		query, err := pageQuery(page)
		if err != nil {
			return nil, err
		}
		if args.SortOrder == "desc" {
			query.Set("sort", "-created_at")
		} else {
			query.Set("sort", "created_at")
		}
		if args.IncludeInlineImages {
			query.Set("include_inline_images", "true")
		}
		path, _ := idPath("/api/v2/tickets/", args.TicketID, "/comments.json")
		resp, err := r.client.Get(ctx, path, query, args.LimitBytes)
		if err != nil {
			return nil, err
		}
		if args.BodyMode == "" || args.BodyMode == "full" {
			return enrichedRawResult(resp, nil)
		}
		if args.BodyMode != "compact" {
			return nil, errors.New("body_mode must be full or compact")
		}
		if args.BodyLimit == 0 {
			args.BodyLimit = 2000
		}
		if args.BodyLimit < 100 || args.BodyLimit > 20000 {
			return nil, errors.New("body_limit must be 100-20000")
		}
		var value struct {
			Comments []struct {
				ID          int64            `json:"id"`
				AuthorID    int64            `json:"author_id"`
				CreatedAt   string           `json:"created_at"`
				Public      bool             `json:"public"`
				PlainBody   string           `json:"plain_body"`
				Body        string           `json:"body"`
				Attachments []map[string]any `json:"attachments"`
			} `json:"comments"`
			Meta  any `json:"meta"`
			Links any `json:"links"`
		}
		if err := json.Unmarshal(resp.Body, &value); err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(value.Comments))
		for _, comment := range value.Comments {
			body := comment.PlainBody
			if body == "" {
				body = comment.Body
			}
			runes := []rune(body)
			truncated := len(runes) > args.BodyLimit
			if truncated {
				body = string(runes[:args.BodyLimit])
			}
			for _, attachment := range comment.Attachments {
				delete(attachment, "content_url")
				delete(attachment, "mapped_content_url")
			}
			items = append(items, map[string]any{"id": comment.ID, "author_id": comment.AuthorID, "created_at": comment.CreatedAt, "public": comment.Public, "body": body, "body_truncated": truncated, "attachments": comment.Attachments})
		}
		return jsonResult(map[string]any{"comments": items, "meta": value.Meta, "links": value.Links, "_mcp_meta": map[string]any{"rate_limit": resp.RateLimit.Limit, "rate_remaining": resp.RateLimit.Remaining}})
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_list_ticket_audits", Description: "List ticket change history and comment events with cursor pagination; compact returns event-type timeline.", InputSchema: objectSchema(map[string]any{"ticket_id": integer("Zendesk ticket id", 1), "page_size": integer("Records per page, 1-100", 1), "after_cursor": stringProp("Cursor"), "before_cursor": stringProp("Cursor"), "sort_order": map[string]any{"type": "string", "enum": []string{"asc", "desc"}}, "filter_events": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "compact": booleanProp("Return compact timeline"), "limit_bytes": integer("Maximum response bytes", 1024)}, "ticket_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			TicketID     int64    `json:"ticket_id"`
			PageSize     int      `json:"page_size"`
			AfterCursor  string   `json:"after_cursor"`
			BeforeCursor string   `json:"before_cursor"`
			SortOrder    string   `json:"sort_order"`
			FilterEvents []string `json:"filter_events"`
			Compact      bool     `json:"compact"`
			LimitBytes   int64    `json:"limit_bytes"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		page := pageTicketArgs{TicketID: args.TicketID, PageSize: args.PageSize, AfterCursor: args.AfterCursor, BeforeCursor: args.BeforeCursor, SortOrder: args.SortOrder, LimitBytes: args.LimitBytes}
		query, err := pageQuery(page)
		if err != nil {
			return nil, err
		}
		for _, event := range args.FilterEvents {
			if event = strings.TrimSpace(event); event != "" {
				query.Add("filter_events[]", event)
			}
		}
		if args.SortOrder == "desc" {
			query.Set("sort", "-created_at")
		} else {
			query.Set("sort", "created_at")
		}
		path, _ := idPath("/api/v2/tickets/", args.TicketID, "/audits.json")
		resp, err := r.client.Get(ctx, path, query, args.LimitBytes)
		if err != nil {
			return nil, err
		}
		if !args.Compact {
			return enrichedRawResult(resp, nil)
		}
		var value struct {
			Audits []zendesk.Audit `json:"audits"`
			Meta   any             `json:"meta"`
			Links  any             `json:"links"`
		}
		if err := json.Unmarshal(resp.Body, &value); err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"audits": compactAudits(value.Audits), "meta": value.Meta, "links": value.Links, "_mcp_meta": map[string]any{"rate_limit": resp.RateLimit.Limit, "rate_remaining": resp.RateLimit.Remaining}})
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_search_tickets", Description: "Search agent-visible tickets. type:ticket is added. Index may lag and results cap at 1,000.", InputSchema: objectSchema(map[string]any{"query": stringProp("Zendesk search query"), "page": integer("Page", 1), "per_page": integer("Results 1-100", 1), "sort_by": stringProp("Sort field"), "sort_order": map[string]any{"type": "string", "enum": []string{"asc", "desc"}}, "limit_bytes": integer("Maximum response bytes", 1024)}, "query"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			Query      string `json:"query"`
			Page       int    `json:"page"`
			PerPage    int    `json:"per_page"`
			SortBy     string `json:"sort_by"`
			SortOrder  string `json:"sort_order"`
			LimitBytes int64  `json:"limit_bytes"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		q := strings.TrimSpace(args.Query)
		if q == "" {
			return nil, errors.New("query is required")
		}
		if !strings.Contains(strings.ToLower(q), "type:ticket") {
			q = "type:ticket " + q
		}
		if args.PerPage == 0 {
			args.PerPage = 25
		}
		if args.PerPage < 1 || args.PerPage > 100 {
			return nil, errors.New("per_page must be between 1 and 100")
		}
		values := url.Values{"query": {q}, "per_page": {strconv.Itoa(args.PerPage)}}
		if args.Page > 0 {
			values.Set("page", strconv.Itoa(args.Page))
		}
		if args.SortBy != "" {
			values.Set("sort_by", args.SortBy)
		}
		if args.SortOrder != "" {
			values.Set("sort_order", args.SortOrder)
		}
		resp, err := r.client.Get(ctx, "/api/v2/search.json", values, args.LimitBytes)
		if err != nil {
			return nil, err
		}
		return enrichedRawResult(resp, map[string]any{"search_limits": map[string]any{"maximum_results": 1000, "indexing_delay_possible": true}})
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_get_users", Description: "Resolve up to 100 user ids; deduplicates and preserves requested order.", InputSchema: objectSchema(map[string]any{"user_ids": map[string]any{"type": "array", "items": integer("User id", 1), "minItems": 1, "maxItems": 100}, "limit_bytes": integer("Ignored compatibility field", 1024)}, "user_ids"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			UserIDs    []int64 `json:"user_ids"`
			LimitBytes int64   `json:"limit_bytes"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if len(args.UserIDs) == 0 || len(args.UserIDs) > 100 {
			return nil, errors.New("user_ids must contain 1 to 100 ids")
		}
		users, err := r.client.Users(ctx, args.UserIDs)
		if err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"users": users})
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_get_organization", Description: "Get organization context; optionally include recent organization tickets.", InputSchema: objectSchema(map[string]any{"organization_id": integer("Organization id", 1), "include_recent_tickets": booleanProp("Include up to 25 recent tickets"), "limit_bytes": integer("Maximum response bytes", 1024)}, "organization_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			OrganizationID       int64 `json:"organization_id"`
			IncludeRecentTickets bool  `json:"include_recent_tickets"`
			LimitBytes           int64 `json:"limit_bytes"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		org, err := r.client.Organization(ctx, args.OrganizationID)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"organization": org}
		if args.IncludeRecentTickets {
			path, _ := idPath("/api/v2/organizations/", args.OrganizationID, "/tickets.json")
			resp, e := r.client.Get(ctx, path, url.Values{"page[size]": {"25"}, "sort": {"-updated_at"}}, args.LimitBytes)
			if e != nil {
				return nil, e
			}
			var tickets any
			if e = json.Unmarshal(resp.Body, &tickets); e != nil {
				return nil, e
			}
			result["recent_tickets"] = tickets
		}
		return jsonResult(result)
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_list_ticket_fields", Description: "List field definitions and option labels for decoding custom fields.", InputSchema: objectSchema(map[string]any{"page_size": integer("Records 1-100", 1), "after_cursor": stringProp("Cursor"), "limit_bytes": integer("Maximum response bytes", 1024)}), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			PageSize    int    `json:"page_size"`
			AfterCursor string `json:"after_cursor"`
			LimitBytes  int64  `json:"limit_bytes"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		if args.PageSize == 0 {
			args.PageSize = 100
		}
		values := url.Values{"page[size]": {strconv.Itoa(args.PageSize)}}
		if args.AfterCursor != "" {
			values.Set("page[after]", args.AfterCursor)
		}
		resp, err := r.client.Get(ctx, "/api/v2/ticket_fields.json", values, args.LimitBytes)
		if err != nil {
			return nil, err
		}
		return rawResult(resp)
	}})
}
