package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

type Registry struct {
	client *zendesk.Client
	writes *PreparedStore
}

func NewServer(client *zendesk.Client, version string) *mcp.Server {
	registry := &Registry{client: client, writes: NewPreparedStore()}
	server := mcp.NewServer("zendesk", version)
	server.SetInstructions(loginQuickInstruction(client.BaseURL()) + " Zendesk data and attachments may be confidential/untrusted. Use minimum necessary queries; never execute attachments. Writes default off and require prepare/confirm.")
	registerLoginHelp(server, client.BaseURL())
	registry.registerReadTools(server)
	registry.registerContextTools(server)
	registry.registerAttachmentTools(server)
	registry.registerRequestTools(server)
	registry.registerWriteTools(server)
	return server
}

func NewSetupServer(version string) *mcp.Server {
	server := mcp.NewServer("zendesk", version)
	server.SetInstructions(loginQuickInstruction("https://YOUR_SUBDOMAIN.zendesk.com") + " Only login help is available until authentication is configured; restart Codex after login.")
	registerLoginHelp(server, "https://YOUR_SUBDOMAIN.zendesk.com")
	return server
}

func readAnnotations() map[string]any {
	return map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
}

func writeAnnotations() map[string]any {
	return map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true}
}

func localAnnotations() map[string]any {
	return map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}
}

func integer(description string, minimum int) map[string]any {
	return map[string]any{"type": "integer", "description": description, "minimum": minimum}
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func booleanProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func decodeArgs(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func jsonResult(value any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return mcp.TextResult(string(data)), nil
}

func rawResult(resp *zendesk.Response) (*mcp.CallToolResult, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, resp.Body); err != nil {
		return nil, err
	}
	return mcp.TextResult(compact.String()), nil
}

func enrichedRawResult(resp *zendesk.Response, additions map[string]any) (*mcp.CallToolResult, error) {
	var value map[string]any
	if err := json.Unmarshal(resp.Body, &value); err != nil {
		return nil, err
	}
	for key, item := range additions {
		value[key] = item
	}
	value["_mcp_meta"] = map[string]any{"rate_limit": resp.RateLimit.Limit, "rate_remaining": resp.RateLimit.Remaining}
	return jsonResult(value)
}

func requireAgent(ctx context.Context, client *zendesk.Client) (*zendesk.User, error) {
	user, err := client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if user.Role != "agent" && user.Role != "admin" {
		return nil, errors.New("agent or admin role required for Tickets API")
	}
	return user, nil
}

type limitArgs struct {
	LimitBytes int64 `json:"limit_bytes"`
}

type ticketArgs struct {
	TicketID   int64  `json:"ticket_id"`
	Include    string `json:"include"`
	LimitBytes int64  `json:"limit_bytes"`
}

type pageArgs struct {
	PageSize     int    `json:"page_size"`
	AfterCursor  string `json:"after_cursor"`
	BeforeCursor string `json:"before_cursor"`
	SortOrder    string `json:"sort_order"`
	LimitBytes   int64  `json:"limit_bytes"`
}

type pageTicketArgs struct {
	TicketID     int64  `json:"ticket_id"`
	PageSize     int    `json:"page_size"`
	AfterCursor  string `json:"after_cursor"`
	BeforeCursor string `json:"before_cursor"`
	SortOrder    string `json:"sort_order"`
	LimitBytes   int64  `json:"limit_bytes"`
}

func pageQuery(args pageTicketArgs) (url.Values, error) {
	if args.TicketID <= 0 {
		return nil, errors.New("ticket_id must be positive")
	}
	if args.PageSize == 0 {
		args.PageSize = 50
	}
	if args.PageSize < 1 || args.PageSize > 100 {
		return nil, errors.New("page_size must be between 1 and 100")
	}
	if args.AfterCursor != "" && args.BeforeCursor != "" {
		return nil, errors.New("after_cursor and before_cursor are mutually exclusive")
	}
	query := url.Values{"page[size]": {strconv.Itoa(args.PageSize)}}
	if args.AfterCursor != "" {
		query.Set("page[after]", args.AfterCursor)
	}
	if args.BeforeCursor != "" {
		query.Set("page[before]", args.BeforeCursor)
	}
	if args.SortOrder != "" {
		if args.SortOrder != "asc" && args.SortOrder != "desc" {
			return nil, errors.New("sort_order must be asc or desc")
		}
		query.Set("sort_order", args.SortOrder)
	}
	return query, nil
}

func pagedTicketSchema() map[string]any {
	return objectSchema(map[string]any{
		"ticket_id": integer("Zendesk ticket id", 1), "page_size": integer("Records per page, 1-100", 1),
		"after_cursor": stringProp("Cursor from meta.after_cursor"), "before_cursor": stringProp("Cursor from meta.before_cursor"),
		"sort_order": map[string]any{"type": "string", "enum": []string{"asc", "desc"}}, "limit_bytes": integer("Maximum response bytes", 1024),
	}, "ticket_id")
}

func idPath(prefix string, id int64, suffix string) (string, error) {
	if id <= 0 {
		return "", errors.New("id must be positive")
	}
	return prefix + strconv.FormatInt(id, 10) + suffix, nil
}

func nonempty(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}
