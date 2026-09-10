package zendesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	resp, err := c.Get(ctx, "/api/v2/users/me.json", nil, 256*1024)
	if err != nil {
		return nil, err
	}
	var result struct {
		User User `json:"user"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, err
	}
	return &result.User, nil
}

func (c *Client) Ticket(ctx context.Context, id int64) (*Ticket, error) {
	if id <= 0 {
		return nil, fmt.Errorf("ticket id must be positive")
	}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/tickets/%d.json", id), nil, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		Ticket Ticket `json:"ticket"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, err
	}
	return &result.Ticket, nil
}

func (c *Client) TicketComments(ctx context.Context, id int64, pageSize int, order string, includeInline bool) ([]Comment, PageMeta, error) {
	if id <= 0 {
		return nil, PageMeta{}, fmt.Errorf("ticket id must be positive")
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	query := url.Values{"page[size]": {strconv.Itoa(pageSize)}, "sort_order": {order}}
	if order == "" {
		query.Set("sort_order", "asc")
	}
	if includeInline {
		query.Set("include_inline_images", "true")
	}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/tickets/%d/comments.json", id), query, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, PageMeta{}, err
	}
	var result struct {
		Comments []Comment `json:"comments"`
		Meta     PageMeta  `json:"meta"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, PageMeta{}, err
	}
	return result.Comments, result.Meta, nil
}

func (c *Client) TicketFields(ctx context.Context) ([]TicketField, error) {
	c.fieldCacheMu.Lock()
	if time.Since(c.fieldCacheAt) < 5*time.Minute && c.fieldCache != nil {
		cached := append([]TicketField(nil), c.fieldCache...)
		c.fieldCacheMu.Unlock()
		return cached, nil
	}
	c.fieldCacheMu.Unlock()
	fields := []TicketField{}
	after := ""
	for page := 0; page < 100; page++ {
		query := url.Values{"page[size]": {"100"}}
		if after != "" {
			query.Set("page[after]", after)
		}
		resp, err := c.Get(ctx, "/api/v2/ticket_fields.json", query, c.cfg.MaxResponseBytes)
		if err != nil {
			return nil, err
		}
		var result struct {
			TicketFields []TicketField `json:"ticket_fields"`
			Meta         PageMeta      `json:"meta"`
		}
		if err := json.Unmarshal(resp.Body, &result); err != nil {
			return nil, err
		}
		fields = append(fields, result.TicketFields...)
		if !result.Meta.HasMore || result.Meta.AfterCursor == "" {
			break
		}
		after = result.Meta.AfterCursor
	}
	c.fieldCacheMu.Lock()
	c.fieldCache = append([]TicketField(nil), fields...)
	c.fieldCacheAt = time.Now()
	c.fieldCacheMu.Unlock()
	return fields, nil
}

func (c *Client) Users(ctx context.Context, ids []int64) ([]User, error) {
	seen := map[int64]bool{}
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			values = append(values, strconv.FormatInt(id, 10))
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	resp, err := c.Get(ctx, "/api/v2/users/show_many.json", url.Values{"ids": {strings.Join(values, ",")}}, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		Users []User `json:"users"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, err
	}
	byID := map[int64]User{}
	for _, user := range result.Users {
		byID[user.ID] = user
	}
	ordered := make([]User, 0, len(values))
	for _, raw := range values {
		id, _ := strconv.ParseInt(raw, 10, 64)
		if user, ok := byID[id]; ok {
			ordered = append(ordered, user)
		}
	}
	return ordered, nil
}

func (c *Client) Organization(ctx context.Context, id int64) (*Organization, error) {
	if id <= 0 {
		return nil, fmt.Errorf("organization id must be positive")
	}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/organizations/%d.json", id), nil, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		Organization Organization `json:"organization"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, err
	}
	return &result.Organization, nil
}

func (c *Client) TicketMetrics(ctx context.Context, id int64) (*TicketMetric, error) {
	if id <= 0 {
		return nil, fmt.Errorf("ticket id must be positive")
	}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/tickets/%d/metrics.json", id), nil, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		TicketMetric TicketMetric `json:"ticket_metric"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, err
	}
	return &result.TicketMetric, nil
}

func (c *Client) RelatedTickets(ctx context.Context, ticket *Ticket, limit int) (map[string]any, error) {
	result := map[string]any{}
	partialErrors := map[string]string{}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/tickets/%d/related.json", ticket.ID), nil, c.cfg.MaxResponseBytes)
	if err == nil {
		var related any
		if json.Unmarshal(resp.Body, &related) == nil {
			result["related"] = related
		}
	} else {
		partialErrors["related"] = err.Error()
	}
	queries := []struct{ Key, Query string }{{"followups", fmt.Sprintf("type:ticket via_followup_source_id:%d", ticket.ID)}}
	if ticket.ProblemID > 0 {
		queries = append(queries, struct{ Key, Query string }{"incidents", fmt.Sprintf("type:ticket problem_id:%d", ticket.ProblemID)})
	}
	if ticket.OrganizationID > 0 {
		queries = append(queries, struct{ Key, Query string }{"same_organization", fmt.Sprintf("type:ticket organization:%d", ticket.OrganizationID)})
	}
	for _, relation := range queries {
		query := url.Values{"query": {relation.Query}, "per_page": {strconv.Itoa(limit)}}
		searchResp, searchErr := c.Get(ctx, "/api/v2/search.json", query, c.cfg.MaxResponseBytes)
		if searchErr != nil {
			partialErrors[relation.Key] = searchErr.Error()
			continue
		}
		var found struct {
			Results []Ticket `json:"results"`
		}
		if json.Unmarshal(searchResp.Body, &found) == nil {
			result[relation.Key] = found.Results
		}
	}
	if len(partialErrors) > 0 {
		result["partial_errors"] = partialErrors
	}
	if err != nil && len(result) == 0 {
		return nil, err
	}
	return result, nil
}

func (c *Client) ValidateAssigneeGroup(ctx context.Context, assigneeID, groupID int64) error {
	if assigneeID <= 0 {
		return nil
	}
	if groupID <= 0 {
		return fmt.Errorf("group_id required when assigning agent")
	}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/users/%d/group_memberships.json", assigneeID), url.Values{"page[size]": {"100"}}, c.cfg.MaxResponseBytes)
	if err != nil {
		return err
	}
	var result struct {
		GroupMemberships []struct {
			GroupID int64 `json:"group_id"`
		} `json:"group_memberships"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return err
	}
	for _, membership := range result.GroupMemberships {
		if membership.GroupID == groupID {
			return nil
		}
	}
	return fmt.Errorf("assignee %d is not member of group %d", assigneeID, groupID)
}

func (c *Client) Views(ctx context.Context, pageSize int, after string) ([]View, PageMeta, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	query := url.Values{"page[size]": {strconv.Itoa(pageSize)}, "active": {"true"}}
	if after != "" {
		query.Set("page[after]", after)
	}
	resp, err := c.Get(ctx, "/api/v2/views.json", query, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, PageMeta{}, err
	}
	var result struct {
		Views []View   `json:"views"`
		Meta  PageMeta `json:"meta"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, PageMeta{}, err
	}
	return result.Views, result.Meta, nil
}

func (c *Client) ViewTickets(ctx context.Context, viewID string, pageSize int, after string) ([]Ticket, PageMeta, error) {
	if viewID == "" {
		return nil, PageMeta{}, fmt.Errorf("view_id is required")
	}
	if viewID != "my" && viewID != "my_groups" && viewID != "incoming" {
		if _, err := strconv.ParseInt(viewID, 10, 64); err != nil {
			return nil, PageMeta{}, fmt.Errorf("invalid view_id")
		}
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	query := url.Values{"page[size]": {strconv.Itoa(pageSize)}}
	if after != "" {
		query.Set("page[after]", after)
	}
	resp, err := c.Get(ctx, "/api/v2/views/"+viewID+"/tickets.json", query, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, PageMeta{}, err
	}
	var result struct {
		Tickets []Ticket `json:"tickets"`
		Meta    PageMeta `json:"meta"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, PageMeta{}, err
	}
	return result.Tickets, result.Meta, nil
}
