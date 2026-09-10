package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
)

func (r *Registry) registerAttachmentTools(server *mcp.Server) {
	schema := objectSchema(map[string]any{"ticket_id": integer("Ticket id", 1), "comment_id": integer("Comment id", 1), "attachment_id": integer("Attachment id", 1)}, "ticket_id", "comment_id", "attachment_id")
	server.RegisterTool(mcp.Tool{Name: "zendesk_get_attachment", Description: "Get metadata and malware scan status after proving attachment belongs to specified accessible ticket comment.", InputSchema: schema, Annotations: readAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			TicketID     int64 `json:"ticket_id"`
			CommentID    int64 `json:"comment_id"`
			AttachmentID int64 `json:"attachment_id"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		attachment, err := r.client.GetAttachment(ctx, args.TicketID, args.CommentID, args.AttachmentID)
		if err != nil {
			return nil, err
		}
		attachment.ContentURL = ""
		attachment.MappedContentURL = ""
		return jsonResult(map[string]any{"attachment": attachment})
	}})
	downloadProps := map[string]any{"ticket_id": integer("Ticket id", 1), "comment_id": integer("Comment id", 1), "attachment_id": integer("Attachment id", 1), "destination": stringProp("Relative path below configured download root; parent directory must exist"), "max_bytes": integer("Maximum bytes, capped by server config", 1)}
	server.RegisterTool(mcp.Tool{Name: "zendesk_download_attachment", Description: "Download malware-cleared attachment into configured root. Never executes or extracts it; returns local path, size, MIME type, and SHA-256.", InputSchema: objectSchema(downloadProps, "ticket_id", "comment_id", "attachment_id", "destination"), Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true}, Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			TicketID     int64  `json:"ticket_id"`
			CommentID    int64  `json:"comment_id"`
			AttachmentID int64  `json:"attachment_id"`
			Destination  string `json:"destination"`
			MaxBytes     int64  `json:"max_bytes"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		attachment, err := r.client.GetAttachment(ctx, args.TicketID, args.CommentID, args.AttachmentID)
		if err != nil {
			return nil, err
		}
		result, err := r.client.DownloadAttachment(ctx, *attachment, args.Destination, args.MaxBytes)
		if err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(result.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve result path: %w", err)
		}
		result.Path = absolute
		return jsonResult(result)
	}})
}
