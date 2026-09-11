package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

const preparedTTL = 10 * time.Minute
const maxCommentBodyBytes = 64 * 1024

var errPreparedOperationUsed = errors.New("prepared operation already used")

type operationPayload struct {
	Kind          string                 `json:"kind"`
	TicketID      int64                  `json:"ticket_id,omitempty"`
	RequestID     int64                  `json:"request_id,omitempty"`
	TicketCreate  *zendesk.TicketCreate  `json:"ticket_create,omitempty"`
	TicketUpdate  *zendesk.TicketUpdate  `json:"ticket_update,omitempty"`
	RequestCreate *zendesk.RequestCreate `json:"request_create,omitempty"`
	RequestUpdate *zendesk.RequestUpdate `json:"request_update,omitempty"`
	Files         []string               `json:"files,omitempty"`
}

type preparedOperation struct {
	ID      string
	Digest  string
	Payload operationPayload
	Tenant  string
	UserID  int64
	Expires time.Time
	Used    bool
}

type PreparedStore struct {
	mu    sync.Mutex
	items map[string]*preparedOperation
	now   func() time.Time
}

func NewPreparedStore() *PreparedStore {
	return &PreparedStore{items: map[string]*preparedOperation{}, now: time.Now}
}

func (s *PreparedStore) Prepare(payload operationPayload, tenant string, userID int64) (*preparedOperation, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	op := &preparedOperation{ID: randomID(), Digest: hex.EncodeToString(sum[:]), Payload: payload, Tenant: tenant, UserID: userID, Expires: s.now().Add(preparedTTL)}
	if op.ID == "" {
		return nil, errors.New("generate operation id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.items[op.ID] = op
	copy := *op
	return &copy, nil
}

func (s *PreparedStore) Checkout(id, digest, tenant string, userID int64, kind string) (*preparedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	op := s.items[id]
	if op == nil {
		return nil, errors.New("prepared operation missing or expired")
	}
	if op.Used {
		return nil, errPreparedOperationUsed
	}
	if op.Digest != digest {
		return nil, errors.New("prepared operation digest mismatch")
	}
	if op.Tenant != tenant || op.UserID != userID {
		return nil, errors.New("prepared operation identity or tenant mismatch")
	}
	if op.Payload.Kind != kind {
		return nil, errors.New("prepared operation kind does not match commit tool")
	}
	op.Used = true
	copy := *op
	return &copy, nil
}

func (s *PreparedStore) Complete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op := s.items[id]; op != nil {
		op.Used = true
	}
}
func (s *PreparedStore) pruneLocked() {
	now := s.now()
	for id, op := range s.items {
		if !op.Expires.After(now) {
			delete(s.items, id)
		}
	}
}
func randomID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return ""
	}
	return hex.EncodeToString(data)
}

type confirmArgs struct {
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
	Confirm     bool   `json:"confirm"`
}

func (r *Registry) registerWriteTools(server *mcp.Server) {
	if !r.client.WriteEnabled() {
		return
	}
	r.registerTicketCreateTools(server)
	r.registerCommentTools(server)
	r.registerTicketUpdateTool(server)
	r.registerAttachmentWriteTool(server)
	r.registerRequestWriteTools(server)
}

func (r *Registry) registerTicketCreateTools(server *mcp.Server) {
	createProps := map[string]any{"subject": stringProp("Ticket subject"), "body": stringProp("Initial comment"), "public": booleanProp("Required explicit visibility"), "requester_id": integer("Requester id", 1), "organization_id": integer("Organization id", 1), "group_id": integer("Group id", 1), "assignee_id": integer("Assignee id", 1), "priority": enum("low", "normal", "high", "urgent"), "type": enum("question", "incident", "problem", "task"), "tags": stringArray(), "custom_fields": fieldArray()}
	server.RegisterTool(mcp.Tool{Name: "zendesk_prepare_ticket_create", Description: "Validate and prepare agent ticket creation. No network mutation. public is required because customer visibility must be explicit.", InputSchema: objectSchema(createProps, "body", "public"), Annotations: localAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		var args ticketCreateArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		ticket, err := validatedTicketCreate(args, 0)
		if err != nil {
			return nil, err
		}
		if err := r.client.ValidateAssigneeGroup(ctx, ticket.AssigneeID, ticket.GroupID); err != nil {
			return nil, err
		}
		return r.prepare(ctx, operationPayload{Kind: "create_ticket", TicketCreate: &ticket}, effectsCreate(ticket))
	}})
	server.RegisterTool(mcp.Tool{Name: "zendesk_create_ticket", Description: "Commit previously prepared ticket creation. Creates permanent ticket, may run triggers and notify users.", InputSchema: confirmSchema(), Annotations: writeAnnotations(), Handler: r.commitHandler("create_ticket")})

	followProps := cloneMap(createProps)
	followProps["source_ticket_id"] = integer("Closed source ticket id", 1)
	server.RegisterTool(mcp.Tool{Name: "zendesk_prepare_followup_ticket", Description: "Prepare follow-up ticket after proving source ticket is closed. No mutation.", InputSchema: objectSchema(followProps, "source_ticket_id", "body", "public"), Annotations: localAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		var args ticketCreateArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		source, err := r.client.Ticket(ctx, args.SourceTicketID)
		if err != nil {
			return nil, err
		}
		if source.Status != "closed" {
			return nil, fmt.Errorf("follow-up source must be closed; current status=%s", source.Status)
		}
		ticket, err := validatedTicketCreate(args, args.SourceTicketID)
		if err != nil {
			return nil, err
		}
		if err := r.client.ValidateAssigneeGroup(ctx, ticket.AssigneeID, ticket.GroupID); err != nil {
			return nil, err
		}
		return r.prepare(ctx, operationPayload{Kind: "create_followup", TicketCreate: &ticket}, append(effectsCreate(ticket), fmt.Sprintf("creates follow-up of closed ticket %d", source.ID)))
	}})
	server.RegisterTool(mcp.Tool{Name: "zendesk_create_followup_ticket", Description: "Commit prepared follow-up creation. Permanent and may notify users/run triggers.", InputSchema: confirmSchema(), Annotations: writeAnnotations(), Handler: r.commitHandler("create_followup")})
}

func (r *Registry) registerCommentTools(server *mcp.Server) {
	props := map[string]any{"ticket_id": integer("Ticket id", 1), "body": stringProp("Comment body"), "operation_id": stringProp("Prepared operation id for commit"), "digest": stringProp("Prepared digest for commit"), "confirm": booleanProp("Must be true to commit")}
	register := func(name, kind, description string, public bool) {
		server.RegisterTool(mcp.Tool{Name: name, Description: description + " First call with ticket_id/body prepares; second call with operation_id/digest/confirm commits.", InputSchema: objectSchema(props), Annotations: writeAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
			if _, err := requireAgent(ctx, r.client); err != nil {
				return nil, err
			}
			var args commentArgs
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			if args.OperationID != "" {
				return r.commit(ctx, kind, confirmArgs{args.OperationID, args.Digest, args.Confirm})
			}
			body, err := nonempty(args.Body, "body")
			if err != nil {
				return nil, err
			}
			if err := validateBody(body); err != nil {
				return nil, err
			}
			ticket, err := r.client.Ticket(ctx, args.TicketID)
			if err != nil {
				return nil, err
			}
			update := zendesk.TicketUpdate{Comment: &zendesk.CommentWrite{Body: body, Public: public}, SafeUpdate: true, UpdatedStamp: ticket.UpdatedAt}
			visibility := "private internal note"
			if public {
				visibility = "PUBLIC customer reply; triggers may send email"
			}
			return r.prepare(ctx, operationPayload{Kind: kind, TicketID: ticket.ID, TicketUpdate: &update}, []string{visibility, fmt.Sprintf("updates ticket %d", ticket.ID), "collision-protected against newer ticket updates"})
		}})
	}
	register("zendesk_add_internal_note", "internal_note", "Add private internal note.", false)
	register("zendesk_add_public_reply", "public_reply", "Add public customer-visible reply.", true)
}

func (r *Registry) registerTicketUpdateTool(server *mcp.Server) {
	props := map[string]any{"ticket_id": integer("Ticket id", 1), "status": enum("new", "open", "pending", "hold", "solved"), "custom_status_id": integer("Custom status id", 1), "priority": enum("low", "normal", "high", "urgent"), "type": enum("question", "incident", "problem", "task"), "group_id": integer("Group id", 1), "assignee_id": integer("Assignee id", 1), "tags": stringArray(), "custom_fields": fieldArray(), "operation_id": stringProp("Prepared operation id"), "digest": stringProp("Prepared digest"), "confirm": booleanProp("Must be true to commit")}
	server.RegisterTool(mcp.Tool{Name: "zendesk_update_ticket", Description: "Prepare or commit allowlisted field changes with safe_update collision protection. Omit operation_id to prepare; provide operation_id/digest/confirm to commit.", InputSchema: objectSchema(props), Annotations: writeAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		var args ticketUpdateArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if args.OperationID != "" {
			return r.commit(ctx, "update_ticket", confirmArgs{args.OperationID, args.Digest, args.Confirm})
		}
		ticket, err := r.client.Ticket(ctx, args.TicketID)
		if err != nil {
			return nil, err
		}
		update := zendesk.TicketUpdate{Status: args.Status, CustomStatusID: args.CustomStatusID, Priority: args.Priority, Type: args.Type, GroupID: args.GroupID, AssigneeID: args.AssigneeID, Tags: args.Tags, CustomFields: args.CustomFields, SafeUpdate: true, UpdatedStamp: ticket.UpdatedAt}
		if err := validateOptionalIDs(args.CustomStatusID, args.GroupID, args.AssigneeID); err != nil {
			return nil, err
		}
		if !oneOfOrEmpty(args.Status, "new", "open", "pending", "hold", "solved") {
			return nil, fmt.Errorf("invalid status %q", args.Status)
		}
		if err := validateTicketValues(args.Priority, args.Type, args.Tags, args.CustomFields); err != nil {
			return nil, err
		}
		resolvedGroup := args.GroupID
		if resolvedGroup == 0 {
			resolvedGroup = ticket.GroupID
		}
		if err := r.client.ValidateAssigneeGroup(ctx, args.AssigneeID, resolvedGroup); err != nil {
			return nil, err
		}
		if !hasTicketChanges(update) {
			return nil, errors.New("at least one writable field change is required")
		}
		effects := []string{fmt.Sprintf("updates ticket %d", ticket.ID), "collision-protected; no automatic retry", "triggers may run"}
		if args.Tags != nil {
			effects = append(effects, "REPLACES entire ticket tag set")
		}
		if args.Status != "" || args.CustomStatusID > 0 {
			effects = append(effects, "status change may alter SLA and send notifications")
		}
		return r.prepare(ctx, operationPayload{Kind: "update_ticket", TicketID: ticket.ID, TicketUpdate: &update}, effects)
	}})
}

func (r *Registry) registerAttachmentWriteTool(server *mcp.Server) {
	props := map[string]any{"ticket_id": integer("Ticket id", 1), "body": stringProp("Comment body"), "visibility": enum("private", "public"), "files": map[string]any{"type": "array", "items": stringProp("Relative path under upload root"), "minItems": 1, "maxItems": 10}, "operation_id": stringProp("Prepared operation id"), "digest": stringProp("Prepared digest"), "confirm": booleanProp("Must be true")}
	server.RegisterTool(mcp.Tool{Name: "zendesk_add_comment_with_attachments", Description: "Prepare or commit comment with uploaded files. Visibility must be explicit. Files remain unexecuted; paths confined to upload root.", InputSchema: objectSchema(props), Annotations: writeAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		if _, err := requireAgent(ctx, r.client); err != nil {
			return nil, err
		}
		var args attachmentCommentArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if args.OperationID != "" {
			return r.commit(ctx, "comment_attachments", confirmArgs{args.OperationID, args.Digest, args.Confirm})
		}
		body, err := nonempty(args.Body, "body")
		if err != nil {
			return nil, err
		}
		if err := validateBody(body); err != nil {
			return nil, err
		}
		if args.Visibility != "private" && args.Visibility != "public" {
			return nil, errors.New("visibility must be private or public")
		}
		if len(args.Files) == 0 || len(args.Files) > 10 {
			return nil, errors.New("files must contain 1-10 paths")
		}
		for _, file := range args.Files {
			if _, err := r.client.ValidateUploadPath(file); err != nil {
				return nil, err
			}
		}
		ticket, err := r.client.Ticket(ctx, args.TicketID)
		if err != nil {
			return nil, err
		}
		update := zendesk.TicketUpdate{Comment: &zendesk.CommentWrite{Body: body, Public: args.Visibility == "public"}, SafeUpdate: true, UpdatedStamp: ticket.UpdatedAt}
		return r.prepare(ctx, operationPayload{Kind: "comment_attachments", TicketID: ticket.ID, TicketUpdate: &update, Files: args.Files}, []string{fmt.Sprintf("uploads %d file(s) and adds %s comment to ticket %d", len(args.Files), args.Visibility, ticket.ID), "public visibility may notify customer", "unused upload tokens cleaned up on failure when possible"})
	}})
}

func (r *Registry) registerRequestWriteTools(server *mcp.Server) {
	server.RegisterTool(mcp.Tool{Name: "zendesk_prepare_request", Description: "Prepare public end-user request creation; no mutation.", InputSchema: objectSchema(map[string]any{"subject": stringProp("Subject"), "body": stringProp("Public initial comment"), "priority": enum("low", "normal", "high", "urgent"), "type": enum("question", "incident", "problem", "task"), "ticket_form_id": integer("Ticket form id", 1), "custom_fields": fieldArray()}, "subject", "body"), Annotations: localAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args requestCreateArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		subject, err := nonempty(args.Subject, "subject")
		if err != nil {
			return nil, err
		}
		body, err := nonempty(args.Body, "body")
		if err != nil {
			return nil, err
		}
		if len([]rune(subject)) > 500 {
			return nil, errors.New("subject exceeds 500 characters")
		}
		if err := validateOptionalIDs(args.TicketFormID); err != nil {
			return nil, err
		}
		if err := validateBody(body); err != nil {
			return nil, err
		}
		if err := validateTicketValues(args.Priority, args.Type, nil, args.CustomFields); err != nil {
			return nil, err
		}
		request := zendesk.RequestCreate{Subject: subject, Comment: zendesk.RequestCommentWrite{Body: body}, Priority: args.Priority, Type: args.Type, TicketFormID: args.TicketFormID, CustomFields: args.CustomFields}
		return r.prepare(ctx, operationPayload{Kind: "create_request", RequestCreate: &request}, []string{"creates public support request", "may notify support users and run triggers"})
	}})
	server.RegisterTool(mcp.Tool{Name: "zendesk_create_request", Description: "Commit prepared public end-user request creation.", InputSchema: confirmSchema(), Annotations: writeAnnotations(), Handler: r.commitHandler("create_request")})
	server.RegisterTool(mcp.Tool{Name: "zendesk_add_request_comment", Description: "Prepare or commit public request comment. Requests API cannot create private notes.", InputSchema: objectSchema(map[string]any{"request_id": integer("Request id", 1), "body": stringProp("Public body"), "operation_id": stringProp("Prepared operation id"), "digest": stringProp("Prepared digest"), "confirm": booleanProp("Must be true")}), Annotations: writeAnnotations(), Handler: func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args requestCommentArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		if args.OperationID != "" {
			return r.commit(ctx, "request_comment", confirmArgs{args.OperationID, args.Digest, args.Confirm})
		}
		body, err := nonempty(args.Body, "body")
		if err != nil {
			return nil, err
		}
		if err := validateBody(body); err != nil {
			return nil, err
		}
		request, err := r.client.Request(ctx, args.RequestID)
		if err != nil {
			return nil, err
		}
		update := zendesk.RequestUpdate{Comment: &zendesk.RequestCommentWrite{Body: body}}
		return r.prepare(ctx, operationPayload{Kind: "request_comment", RequestID: request.ID, RequestUpdate: &update}, []string{fmt.Sprintf("adds PUBLIC comment to request %d", request.ID), "may notify support users"})
	}})
}

func (r *Registry) prepare(ctx context.Context, payload operationPayload, effects []string) (*mcp.CallToolResult, error) {
	user, err := r.client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if payload.Kind != "create_request" && payload.Kind != "request_comment" && user.Role != "agent" && user.Role != "admin" {
		return nil, errors.New("agent or admin role required for Tickets API write")
	}
	op, err := r.writes.Prepare(payload, r.client.BaseURL(), user.ID)
	if err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"prepared": true, "operation_id": op.ID, "digest": op.Digest, "kind": payload.Kind, "expires_at": op.Expires.UTC().Format(time.RFC3339), "effects": effects, "next": "review effects, then call commit tool with operation_id, digest, and confirm=true"})
}
func (r *Registry) commitHandler(kind string) mcp.Handler {
	return func(ctx context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args confirmArgs
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
		return r.commit(ctx, kind, args)
	}
}
func (r *Registry) commit(ctx context.Context, kind string, args confirmArgs) (*mcp.CallToolResult, error) {
	if !args.Confirm {
		return nil, errors.New("confirm must be true")
	}
	user, err := r.client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if kind != "create_request" && kind != "request_comment" && user.Role != "agent" && user.Role != "admin" {
		return nil, errors.New("agent or admin role required for Tickets API write")
	}
	op, err := r.writes.Checkout(args.OperationID, args.Digest, r.client.BaseURL(), user.ID, kind)
	if err != nil {
		if errors.Is(err, errPreparedOperationUsed) {
			return nil, writeAttemptConsumedError(err)
		}
		return nil, err
	}
	resp, err := r.execute(ctx, op.Payload)
	if err != nil {
		log.Printf("zendesk_write kind=%s operation=%s digest=%s user=%d outcome=unknown", kind, op.ID, op.Digest, user.ID)
		return nil, writeAttemptConsumedError(err)
	}
	if resp == nil {
		log.Printf("zendesk_write kind=%s operation=%s digest=%s user=%d outcome=unknown", kind, op.ID, op.Digest, user.ID)
		return nil, writeAttemptConsumedError(errors.New("write returned no response"))
	}
	r.writes.Complete(op.ID)
	result, err := rawResult(resp)
	if err != nil {
		log.Printf("zendesk_write kind=%s operation=%s digest=%s user=%d status=%d outcome=unusable_response", kind, op.ID, op.Digest, user.ID, resp.StatusCode)
		return nil, writeAttemptConsumedError(fmt.Errorf("decode write response: %w", err))
	}
	log.Printf("zendesk_write kind=%s operation=%s digest=%s user=%d status=%d", kind, op.ID, op.Digest, user.ID, resp.StatusCode)
	return result, nil
}

func writeAttemptConsumedError(err error) error {
	return fmt.Errorf("WRITE_ATTEMPT_CONSUMED: do not retry this operation; reconcile Zendesk state before preparing another write: %w", err)
}

func (r *Registry) execute(ctx context.Context, p operationPayload) (*zendesk.Response, error) {
	switch p.Kind {
	case "create_ticket", "create_followup":
		return r.client.CreateTicket(ctx, *p.TicketCreate)
	case "internal_note", "public_reply", "update_ticket":
		return r.client.UpdateTicket(ctx, p.TicketID, *p.TicketUpdate)
	case "comment_attachments":
		tokens := []string{}
		for _, file := range p.Files {
			upload, err := r.client.UploadFile(ctx, file)
			if err != nil {
				r.cleanupUploads(ctx, tokens)
				return nil, err
			}
			tokens = append(tokens, upload.Token)
		}
		p.TicketUpdate.Comment.Uploads = tokens
		resp, err := r.client.UpdateTicket(ctx, p.TicketID, *p.TicketUpdate)
		// Redirects, missing responses, unusable 2xx responses, and 5xx responses
		// may all follow a committed update, so only clean up after a clear rejection.
		if err != nil && resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			r.cleanupUploads(ctx, tokens)
		}
		return resp, err
	case "create_request":
		return r.client.CreateRequest(ctx, *p.RequestCreate)
	case "request_comment":
		return r.client.UpdateRequest(ctx, p.RequestID, *p.RequestUpdate)
	default:
		return nil, errors.New("unsupported prepared operation kind")
	}
}
func (r *Registry) cleanupUploads(ctx context.Context, tokens []string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for index, token := range tokens {
		if err := r.client.DeleteUpload(cleanupCtx, token); err != nil {
			log.Printf("zendesk_upload_cleanup index=%d outcome=failed error_type=%T", index, err)
		}
	}
}

type ticketCreateArgs struct {
	Subject        string               `json:"subject"`
	Body           string               `json:"body"`
	Public         *bool                `json:"public"`
	RequesterID    int64                `json:"requester_id"`
	OrganizationID int64                `json:"organization_id"`
	GroupID        int64                `json:"group_id"`
	AssigneeID     int64                `json:"assignee_id"`
	Priority       string               `json:"priority"`
	Type           string               `json:"type"`
	Tags           []string             `json:"tags"`
	CustomFields   []zendesk.FieldWrite `json:"custom_fields"`
	SourceTicketID int64                `json:"source_ticket_id"`
}
type ticketUpdateArgs struct {
	TicketID       int64                `json:"ticket_id"`
	Status         string               `json:"status"`
	CustomStatusID int64                `json:"custom_status_id"`
	Priority       string               `json:"priority"`
	Type           string               `json:"type"`
	GroupID        int64                `json:"group_id"`
	AssigneeID     int64                `json:"assignee_id"`
	Tags           []string             `json:"tags"`
	CustomFields   []zendesk.FieldWrite `json:"custom_fields"`
	OperationID    string               `json:"operation_id"`
	Digest         string               `json:"digest"`
	Confirm        bool                 `json:"confirm"`
}
type commentArgs struct {
	TicketID    int64  `json:"ticket_id"`
	Body        string `json:"body"`
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
	Confirm     bool   `json:"confirm"`
}
type attachmentCommentArgs struct {
	TicketID    int64    `json:"ticket_id"`
	Body        string   `json:"body"`
	Visibility  string   `json:"visibility"`
	Files       []string `json:"files"`
	OperationID string   `json:"operation_id"`
	Digest      string   `json:"digest"`
	Confirm     bool     `json:"confirm"`
}
type requestCreateArgs struct {
	Subject      string               `json:"subject"`
	Body         string               `json:"body"`
	Priority     string               `json:"priority"`
	Type         string               `json:"type"`
	TicketFormID int64                `json:"ticket_form_id"`
	CustomFields []zendesk.FieldWrite `json:"custom_fields"`
}
type requestCommentArgs struct {
	RequestID   int64  `json:"request_id"`
	Body        string `json:"body"`
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
	Confirm     bool   `json:"confirm"`
}

func validatedTicketCreate(args ticketCreateArgs, followup int64) (zendesk.TicketCreate, error) {
	body, err := nonempty(args.Body, "body")
	if err != nil {
		return zendesk.TicketCreate{}, err
	}
	subject := strings.TrimSpace(args.Subject)
	if args.Public == nil {
		return zendesk.TicketCreate{}, errors.New("public is required and must explicitly be true or false")
	}
	if err := validateBody(body); err != nil {
		return zendesk.TicketCreate{}, err
	}
	if len([]rune(subject)) > 500 {
		return zendesk.TicketCreate{}, errors.New("subject exceeds 500 characters")
	}
	if followup == 0 && subject == "" {
		return zendesk.TicketCreate{}, errors.New("subject is required for new ticket")
	}
	if args.AssigneeID > 0 && args.GroupID == 0 {
		return zendesk.TicketCreate{}, errors.New("group_id required when assigning agent")
	}
	if err := validateOptionalIDs(args.RequesterID, args.OrganizationID, args.GroupID, args.AssigneeID, followup); err != nil {
		return zendesk.TicketCreate{}, err
	}
	if err := validateTicketValues(args.Priority, args.Type, args.Tags, args.CustomFields); err != nil {
		return zendesk.TicketCreate{}, err
	}
	return zendesk.TicketCreate{Subject: subject, Comment: zendesk.CommentWrite{Body: body, Public: *args.Public}, RequesterID: args.RequesterID, OrganizationID: args.OrganizationID, GroupID: args.GroupID, AssigneeID: args.AssigneeID, Priority: args.Priority, Type: args.Type, Tags: args.Tags, CustomFields: args.CustomFields, ViaFollowupSourceID: followup}, nil
}
func effectsCreate(t zendesk.TicketCreate) []string {
	visibility := "private initial comment"
	if t.Comment.Public {
		visibility = "PUBLIC initial comment; customer notifications may fire"
	}
	return []string{"creates permanent Zendesk ticket", visibility, "triggers and SLA policies may run"}
}
func hasTicketChanges(u zendesk.TicketUpdate) bool {
	return u.Status != "" || u.CustomStatusID > 0 || u.Priority != "" || u.Type != "" || u.GroupID > 0 || u.AssigneeID > 0 || u.Tags != nil || u.CustomFields != nil
}
func validateTicketValues(priority, ticketType string, tags []string, fields []zendesk.FieldWrite) error {
	if !oneOfOrEmpty(priority, "low", "normal", "high", "urgent") {
		return fmt.Errorf("invalid priority %q", priority)
	}
	if !oneOfOrEmpty(ticketType, "question", "incident", "problem", "task") {
		return fmt.Errorf("invalid type %q", ticketType)
	}
	if len(tags) > 100 {
		return errors.New("too many tags")
	}
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 255 {
			return errors.New("tags must be non-empty and at most 255 bytes")
		}
	}
	if len(fields) > 100 {
		return errors.New("too many custom fields")
	}
	for _, field := range fields {
		if field.ID <= 0 {
			return errors.New("custom field ids must be positive")
		}
		if !safeFieldValue(field.Value) {
			return fmt.Errorf("custom field %d has unsupported object value", field.ID)
		}
	}
	return nil
}
func validateOptionalIDs(ids ...int64) error {
	for _, id := range ids {
		if id < 0 {
			return errors.New("optional ids cannot be negative")
		}
	}
	return nil
}
func validateBody(body string) error {
	if len([]byte(body)) > maxCommentBodyBytes {
		return fmt.Errorf("body exceeds Zendesk %d-byte comment limit", maxCommentBodyBytes)
	}
	return nil
}
func oneOfOrEmpty(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
func safeFieldValue(value any) bool {
	switch typed := value.(type) {
	case nil, string, float64, bool:
		return true
	case []any:
		for _, item := range typed {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}
func confirmSchema() map[string]any {
	return objectSchema(map[string]any{"operation_id": stringProp("Prepared operation id"), "digest": stringProp("Exact prepared digest"), "confirm": booleanProp("Must be true")}, "operation_id", "digest", "confirm")
}
func enum(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
func stringArray() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100}
}
func fieldArray() map[string]any {
	return map[string]any{"type": "array", "items": objectSchema(map[string]any{"id": integer("Field id", 1), "value": map[string]any{}}, "id", "value"), "maxItems": 100}
}
func cloneMap(source map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range source {
		result[key] = value
	}
	return result
}
