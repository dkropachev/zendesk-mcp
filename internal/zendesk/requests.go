package zendesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

func (c *Client) ListRequests(ctx context.Context, pageSize int, after string) ([]Request, PageMeta, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	query := url.Values{"page[size]": {strconv.Itoa(pageSize)}}
	if after != "" {
		query.Set("page[after]", after)
	}
	resp, err := c.Get(ctx, "/api/v2/requests.json", query, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, PageMeta{}, err
	}
	var result struct {
		Requests []Request `json:"requests"`
		Meta     PageMeta  `json:"meta"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, PageMeta{}, err
	}
	return result.Requests, result.Meta, nil
}

func (c *Client) Request(ctx context.Context, id int64) (*Request, error) {
	if id <= 0 {
		return nil, fmt.Errorf("request id must be positive")
	}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/requests/%d.json", id), nil, c.cfg.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		Request Request `json:"request"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, err
	}
	return &result.Request, nil
}

func (c *Client) RequestComments(ctx context.Context, id int64, pageSize int, after string) ([]Comment, PageMeta, error) {
	if id <= 0 {
		return nil, PageMeta{}, fmt.Errorf("request id must be positive")
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	query := url.Values{"page[size]": {strconv.Itoa(pageSize)}}
	if after != "" {
		query.Set("page[after]", after)
	}
	resp, err := c.Get(ctx, fmt.Sprintf("/api/v2/requests/%d/comments.json", id), query, c.cfg.MaxResponseBytes)
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
