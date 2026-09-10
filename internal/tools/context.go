package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

type decodedField struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Value any    `json:"value"`
	Label any    `json:"label,omitempty"`
}

func (r *Registry) registerContextTools(server *mcp.Server) {
	server.RegisterTool(mcp.Tool{Name: "zendesk_get_ticket_context", Description: "Build bounded investigation context: ticket, readable custom fields, conversation, people, organization, related tickets, and optional metrics/audits.", InputSchema: objectSchema(map[string]any{
		"ticket_id": integer("Ticket id", 1), "comment_limit": integer("Maximum comments 1-100", 1), "include_private_comments": booleanProp("Include internal notes; defaults true for agents"), "include_audits": booleanProp("Include compact audit timeline"), "include_metrics": booleanProp("Include operational ticket metrics"), "include_related": booleanProp("Include related/follow-up/same-organization tickets; defaults true"), "body_limit": integer("Characters retained per comment body; default 4000", 100),
	}, "ticket_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			TicketID               int64 `json:"ticket_id"`
			CommentLimit           int   `json:"comment_limit"`
			IncludePrivateComments *bool `json:"include_private_comments"`
			IncludeAudits          bool  `json:"include_audits"`
			IncludeMetrics         bool  `json:"include_metrics"`
			IncludeRelated         *bool `json:"include_related"`
			BodyLimit              int   `json:"body_limit"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		if args.TicketID <= 0 {
			return nil, fmt.Errorf("ticket_id must be positive")
		}
		if args.CommentLimit == 0 {
			args.CommentLimit = 100
		}
		if args.CommentLimit < 1 || args.CommentLimit > 100 {
			return nil, fmt.Errorf("comment_limit must be 1-100")
		}
		if args.BodyLimit == 0 {
			args.BodyLimit = 4000
		}
		if args.BodyLimit < 100 || args.BodyLimit > 20000 {
			return nil, fmt.Errorf("body_limit must be 100-20000")
		}
		includePrivate := true
		if args.IncludePrivateComments != nil {
			includePrivate = *args.IncludePrivateComments
		}
		includeRelated := true
		if args.IncludeRelated != nil {
			includeRelated = *args.IncludeRelated
		}
		ticket, err := r.client.Ticket(ctx, args.TicketID)
		if err != nil {
			return nil, err
		}
		comments, meta, err := r.client.TicketComments(ctx, args.TicketID, args.CommentLimit, "asc", true)
		if err != nil {
			return nil, err
		}
		fields, err := r.client.TicketFields(ctx)
		if err != nil {
			return nil, err
		}
		userIDs := []int64{ticket.RequesterID, ticket.SubmitterID, ticket.AssigneeID}
		for _, id := range ticket.CollaboratorIDs {
			userIDs = append(userIDs, id)
		}
		for _, id := range ticket.FollowerIDs {
			userIDs = append(userIDs, id)
		}
		for _, comment := range comments {
			userIDs = append(userIDs, comment.AuthorID)
		}
		users, err := r.client.Users(ctx, userIDs)
		if err != nil {
			return nil, err
		}
		usersByID := map[int64]zendesk.User{}
		for _, user := range users {
			usersByID[user.ID] = user
		}
		visible := make([]map[string]any, 0, len(comments))
		for _, comment := range comments {
			if !includePrivate && !comment.Public {
				continue
			}
			body := comment.PlainBody
			if body == "" {
				body = comment.Body
			}
			truncated := false
			if len([]rune(body)) > args.BodyLimit {
				body = string([]rune(body)[:args.BodyLimit])
				truncated = true
			}
			visible = append(visible, map[string]any{"id": comment.ID, "created_at": comment.CreatedAt, "public": comment.Public, "author": usersByID[comment.AuthorID], "body": body, "body_truncated": truncated, "attachments": safeAttachments(comment.Attachments)})
		}
		result := map[string]any{"ticket": ticket, "agent_url": strings.TrimRight(r.client.BaseURL(), "/") + fmt.Sprintf("/agent/tickets/%d", ticket.ID), "custom_fields": decodeCustomFields(ticket.CustomFields, fields), "comments": visible, "comments_page": meta, "comments_returned": len(visible), "users": users, "metrics_enabled": args.IncludeMetrics, "audits_enabled": args.IncludeAudits}
		if ticket.OrganizationID > 0 {
			org, e := r.client.Organization(ctx, ticket.OrganizationID)
			if e == nil {
				result["organization"] = org
			} else {
				result["organization_error"] = e.Error()
			}
		}
		if args.IncludeMetrics {
			metric, e := r.client.TicketMetrics(ctx, ticket.ID)
			if e != nil {
				return nil, e
			}
			result["metrics"] = metric
		}
		if includeRelated {
			related, e := r.client.RelatedTickets(ctx, ticket, 10)
			if e != nil {
				result["related_error"] = e.Error()
			} else {
				result["related"] = related
			}
		}
		if args.IncludeAudits {
			resp, e := r.client.Get(ctx, fmt.Sprintf("/api/v2/tickets/%d/audits.json", ticket.ID), mapValues("page[size]", "100"), r.client.Config().MaxResponseBytes)
			if e != nil {
				return nil, e
			}
			var audits struct {
				Audits []zendesk.Audit  `json:"audits"`
				Meta   zendesk.PageMeta `json:"meta"`
			}
			if e = json.Unmarshal(resp.Body, &audits); e != nil {
				return nil, e
			}
			result["audits"] = compactAudits(audits.Audits)
			result["audits_page"] = audits.Meta
		}
		return jsonResult(result)
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_list_related_tickets", Description: "Find built-in related data, follow-ups, problem incidents, and recent same-organization tickets.", InputSchema: objectSchema(map[string]any{"ticket_id": integer("Ticket id", 1), "limit": integer("Results per relation, 1-25", 1)}, "ticket_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			TicketID int64 `json:"ticket_id"`
			Limit    int   `json:"limit"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		if args.Limit == 0 {
			args.Limit = 10
		}
		if args.Limit < 1 || args.Limit > 25 {
			return nil, fmt.Errorf("limit must be 1-25")
		}
		ticket, err := r.client.Ticket(ctx, args.TicketID)
		if err != nil {
			return nil, err
		}
		related, err := r.client.RelatedTickets(ctx, ticket, args.Limit)
		if err != nil {
			return nil, err
		}
		return jsonResult(related)
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_get_ticket_metrics", Description: "Get reply, wait, assignment, reopen, and resolution timing for one ticket.", InputSchema: objectSchema(map[string]any{"ticket_id": integer("Ticket id", 1)}, "ticket_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			TicketID int64 `json:"ticket_id"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		metric, err := r.client.TicketMetrics(ctx, args.TicketID)
		if err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"ticket_metric": metric})
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_list_views", Description: "List active queues/views available to current agent.", InputSchema: objectSchema(map[string]any{"page_size": integer("Views 1-100", 1), "after_cursor": stringProp("Cursor")}), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args pageArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		views, meta, err := r.client.Views(ctx, args.PageSize, args.AfterCursor)
		if err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"views": views, "meta": meta})
	}})

	server.RegisterTool(mcp.Tool{Name: "zendesk_list_view_tickets", Description: "Browse tickets in my, my_groups, incoming, or numeric Zendesk view.", InputSchema: objectSchema(map[string]any{"view_id": stringProp("my, my_groups, incoming, or numeric id"), "page_size": integer("Tickets 1-100", 1), "after_cursor": stringProp("Cursor")}, "view_id"), Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			ViewID      string `json:"view_id"`
			PageSize    int    `json:"page_size"`
			AfterCursor string `json:"after_cursor"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		tickets, meta, err := r.client.ViewTickets(ctx, args.ViewID, args.PageSize, args.AfterCursor)
		if err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"tickets": tickets, "meta": meta})
	}})
}

func decodeCustomFields(values []zendesk.CustomFieldValue, fields []zendesk.TicketField) []decodedField {
	definitions := map[int64]zendesk.TicketField{}
	for _, field := range fields {
		definitions[field.ID] = field
	}
	result := make([]decodedField, 0, len(values))
	for _, value := range values {
		definition := definitions[value.ID]
		decoded := decodedField{ID: value.ID, Title: definition.Title, Value: value.Value}
		if decoded.Title == "" {
			decoded.Title = fmt.Sprintf("field_%d", value.ID)
		}
		options := map[string]string{}
		for _, option := range definition.CustomOptions {
			options[option.Value] = option.Name
		}
		switch typed := value.Value.(type) {
		case string:
			if label := options[typed]; label != "" {
				decoded.Label = label
			}
		case []any:
			labels := []string{}
			for _, item := range typed {
				if raw, ok := item.(string); ok {
					if label := options[raw]; label != "" {
						labels = append(labels, label)
					}
				}
			}
			if len(labels) > 0 {
				decoded.Label = labels
			}
		}
		result = append(result, decoded)
	}
	return result
}

func compactAudits(audits []zendesk.Audit) []map[string]any {
	result := make([]map[string]any, 0, len(audits))
	for _, audit := range audits {
		types := []string{}
		for _, event := range audit.Events {
			var item struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(event, &item) == nil {
				types = append(types, item.Type)
			}
		}
		result = append(result, map[string]any{"id": audit.ID, "author_id": audit.AuthorID, "created_at": audit.CreatedAt, "event_types": types})
	}
	return result
}

func mapValues(key, value string) map[string][]string { return map[string][]string{key: {value}} }

func safeAttachments(items []zendesk.Attachment) []zendesk.Attachment {
	result := make([]zendesk.Attachment, len(items))
	copy(result, items)
	for index := range result {
		result[index].ContentURL = ""
		result[index].MappedContentURL = ""
		result[index].Thumbnails = safeAttachments(result[index].Thumbnails)
	}
	return result
}
