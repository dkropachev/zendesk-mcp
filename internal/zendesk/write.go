package zendesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type CommentWrite struct {
	Body    string   `json:"body"`
	Public  bool     `json:"public"`
	Uploads []string `json:"uploads,omitempty"`
}

type FieldWrite struct {
	ID    int64 `json:"id"`
	Value any   `json:"value"`
}

type RequestCommentWrite struct {
	Body    string   `json:"body"`
	Uploads []string `json:"uploads,omitempty"`
}

type TicketCreate struct {
	Subject             string       `json:"subject,omitempty"`
	Comment             CommentWrite `json:"comment"`
	RequesterID         int64        `json:"requester_id,omitempty"`
	OrganizationID      int64        `json:"organization_id,omitempty"`
	GroupID             int64        `json:"group_id,omitempty"`
	AssigneeID          int64        `json:"assignee_id,omitempty"`
	Priority            string       `json:"priority,omitempty"`
	Type                string       `json:"type,omitempty"`
	Tags                []string     `json:"tags,omitempty"`
	CustomFields        []FieldWrite `json:"custom_fields,omitempty"`
	ViaFollowupSourceID int64        `json:"via_followup_source_id,omitempty"`
}

type TicketUpdate struct {
	Comment        *CommentWrite `json:"comment,omitempty"`
	Status         string        `json:"status,omitempty"`
	CustomStatusID int64         `json:"custom_status_id,omitempty"`
	Priority       string        `json:"priority,omitempty"`
	Type           string        `json:"type,omitempty"`
	GroupID        int64         `json:"group_id,omitempty"`
	AssigneeID     int64         `json:"assignee_id,omitempty"`
	Tags           []string      `json:"tags,omitempty"`
	CustomFields   []FieldWrite  `json:"custom_fields,omitempty"`
	SafeUpdate     bool          `json:"safe_update"`
	UpdatedStamp   string        `json:"updated_stamp"`
}

type RequestCreate struct {
	Subject      string              `json:"subject"`
	Comment      RequestCommentWrite `json:"comment"`
	Priority     string              `json:"priority,omitempty"`
	Type         string              `json:"type,omitempty"`
	TicketFormID int64               `json:"ticket_form_id,omitempty"`
	CustomFields []FieldWrite        `json:"custom_fields,omitempty"`
}

type RequestUpdate struct {
	Comment *RequestCommentWrite `json:"comment,omitempty"`
	Solved  *bool                `json:"solved,omitempty"`
}

type UploadResult struct {
	Token      string     `json:"token"`
	Attachment Attachment `json:"attachment"`
}

func (c *Client) CreateTicket(ctx context.Context, ticket TicketCreate) (*Response, error) {
	return c.DoJSON(ctx, http.MethodPost, "/api/v2/tickets.json", nil, struct {
		Ticket TicketCreate `json:"ticket"`
	}{ticket}, c.cfg.MaxResponseBytes)
}

func (c *Client) UpdateTicket(ctx context.Context, id int64, update TicketUpdate) (*Response, error) {
	if id <= 0 {
		return nil, errors.New("ticket id must be positive")
	}
	return c.DoJSON(ctx, http.MethodPut, fmt.Sprintf("/api/v2/tickets/%d.json", id), nil, struct {
		Ticket TicketUpdate `json:"ticket"`
	}{update}, c.cfg.MaxResponseBytes)
}

func (c *Client) CreateRequest(ctx context.Context, request RequestCreate) (*Response, error) {
	return c.DoJSON(ctx, http.MethodPost, "/api/v2/requests.json", nil, struct {
		Request RequestCreate `json:"request"`
	}{request}, c.cfg.MaxResponseBytes)
}

func (c *Client) UpdateRequest(ctx context.Context, id int64, update RequestUpdate) (*Response, error) {
	if id <= 0 {
		return nil, errors.New("request id must be positive")
	}
	return c.DoJSON(ctx, http.MethodPut, fmt.Sprintf("/api/v2/requests/%d.json", id), nil, struct {
		Request RequestUpdate `json:"request"`
	}{update}, c.cfg.MaxResponseBytes)
}

func (c *Client) ValidateUploadPath(relative string) (string, error) {
	if c.cfg.UploadRoot == "" {
		return "", errors.New("uploads disabled: configure ZENDESK_UPLOAD_ROOT")
	}
	path, err := resolveExistingRootFile(c.cfg.UploadRoot, relative)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("upload source must be regular file")
	}
	if info.Size() > maxZendeskUploadBytes {
		return "", fmt.Errorf("upload exceeds Zendesk 50 MiB limit")
	}
	return path, nil
}

func (c *Client) UploadFile(ctx context.Context, relative string) (*UploadResult, error) {
	if !c.cfg.EnableWrite {
		return nil, errors.New("WRITE_DISABLED")
	}
	path, err := c.ValidateUploadPath(relative)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	endpoint, err := c.apiEndpoint("/api/v2/uploads.json", url.Values{"filename": {filepath.Base(path)}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), io.LimitReader(file, maxZendeskUploadBytes+1))
	if err != nil {
		return nil, err
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = info.Size()
	if err := c.applyAuth(req); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > c.cfg.MaxResponseBytes {
		return nil, errors.New("upload response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, newAPIError(resp.StatusCode, body, resp.Header)
	}
	var result struct {
		Upload UploadResult `json:"upload"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Upload.Token == "" {
		return nil, errors.New("upload response missing token")
	}
	return &result.Upload, nil
}

func (c *Client) DeleteUpload(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := c.DoJSON(ctx, http.MethodDelete, "/api/v2/uploads/"+url.PathEscape(token)+".json", nil, nil, 256*1024)
	return err
}

func resolveExistingRootFile(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("upload source must be relative to configured upload root")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("upload source escapes configured root")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve upload root: %w", err)
	}
	pathReal, err := filepath.EvalSymlinks(filepath.Join(rootReal, clean))
	if err != nil {
		return "", fmt.Errorf("resolve upload source: %w", err)
	}
	if !pathWithin(rootReal, pathReal) {
		return "", errors.New("upload source escapes configured root through symlink")
	}
	return pathReal, nil
}
