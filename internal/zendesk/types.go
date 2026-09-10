package zendesk

import "encoding/json"

type User struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role"`
	Active bool   `json:"active"`
}

type Organization struct {
	ID      int64          `json:"id"`
	Name    string         `json:"name"`
	Details string         `json:"details,omitempty"`
	Notes   string         `json:"notes,omitempty"`
	Tags    []string       `json:"tags,omitempty"`
	Fields  map[string]any `json:"organization_fields,omitempty"`
}

type CustomFieldValue struct {
	ID    int64 `json:"id"`
	Value any   `json:"value"`
}

type Ticket struct {
	ID                 int64              `json:"id"`
	URL                string             `json:"url,omitempty"`
	Subject            string             `json:"subject"`
	Description        string             `json:"description,omitempty"`
	Status             string             `json:"status"`
	CustomStatusID     int64              `json:"custom_status_id,omitempty"`
	Type               string             `json:"type,omitempty"`
	Priority           string             `json:"priority,omitempty"`
	RequesterID        int64              `json:"requester_id,omitempty"`
	SubmitterID        int64              `json:"submitter_id,omitempty"`
	AssigneeID         int64              `json:"assignee_id,omitempty"`
	OrganizationID     int64              `json:"organization_id,omitempty"`
	GroupID            int64              `json:"group_id,omitempty"`
	ProblemID          int64              `json:"problem_id,omitempty"`
	TicketFormID       int64              `json:"ticket_form_id,omitempty"`
	CreatedAt          string             `json:"created_at,omitempty"`
	UpdatedAt          string             `json:"updated_at,omitempty"`
	Tags               []string           `json:"tags,omitempty"`
	CollaboratorIDs    []int64            `json:"collaborator_ids,omitempty"`
	FollowerIDs        []int64            `json:"follower_ids,omitempty"`
	EmailCCIDs         []int64            `json:"email_cc_ids,omitempty"`
	CustomFields       []CustomFieldValue `json:"custom_fields,omitempty"`
	Via                json.RawMessage    `json:"via,omitempty"`
	SatisfactionRating json.RawMessage    `json:"satisfaction_rating,omitempty"`
}

type Comment struct {
	ID          int64           `json:"id"`
	AuthorID    int64           `json:"author_id"`
	CreatedAt   string          `json:"created_at"`
	Public      bool            `json:"public"`
	Body        string          `json:"body,omitempty"`
	PlainBody   string          `json:"plain_body,omitempty"`
	HTMLBody    string          `json:"html_body,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Via         json.RawMessage `json:"via,omitempty"`
}

type TicketField struct {
	ID            int64         `json:"id"`
	Title         string        `json:"title"`
	Type          string        `json:"type"`
	Active        bool          `json:"active"`
	CustomOptions []FieldOption `json:"custom_field_options,omitempty"`
}

type FieldOption struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type PageMeta struct {
	HasMore      bool   `json:"has_more"`
	AfterCursor  string `json:"after_cursor,omitempty"`
	BeforeCursor string `json:"before_cursor,omitempty"`
}

type Audit struct {
	ID        int64             `json:"id"`
	TicketID  int64             `json:"ticket_id"`
	AuthorID  int64             `json:"author_id"`
	CreatedAt string            `json:"created_at"`
	Events    []json.RawMessage `json:"events"`
}

type TicketMetric struct {
	ID                     int64           `json:"id"`
	TicketID               int64           `json:"ticket_id"`
	AssignedAt             string          `json:"assigned_at,omitempty"`
	InitiallyAssignedAt    string          `json:"initially_assigned_at,omitempty"`
	LatestCommentAddedAt   string          `json:"latest_comment_added_at,omitempty"`
	SolvedAt               string          `json:"solved_at,omitempty"`
	StatusUpdatedAt        string          `json:"status_updated_at,omitempty"`
	AssigneeStations       int             `json:"assignee_stations,omitempty"`
	GroupStations          int             `json:"group_stations,omitempty"`
	Reopens                int             `json:"reopens,omitempty"`
	Replies                int             `json:"replies,omitempty"`
	ReplyTimeInMinutes     json.RawMessage `json:"reply_time_in_minutes,omitempty"`
	RequesterWaitInMinutes json.RawMessage `json:"requester_wait_time_in_minutes,omitempty"`
	AgentWaitInMinutes     json.RawMessage `json:"agent_wait_time_in_minutes,omitempty"`
	FullResolutionMinutes  json.RawMessage `json:"full_resolution_time_in_minutes,omitempty"`
}

type View struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Active      bool   `json:"active"`
	Default     bool   `json:"default"`
}

type Request struct {
	ID              int64              `json:"id"`
	Subject         string             `json:"subject"`
	Description     string             `json:"description,omitempty"`
	Status          string             `json:"status"`
	Priority        string             `json:"priority,omitempty"`
	Type            string             `json:"type,omitempty"`
	RequesterID     int64              `json:"requester_id,omitempty"`
	OrganizationID  int64              `json:"organization_id,omitempty"`
	AssigneeID      int64              `json:"assignee_id,omitempty"`
	TicketFormID    int64              `json:"ticket_form_id,omitempty"`
	CustomStatusID  int64              `json:"custom_status_id,omitempty"`
	CustomFields    []CustomFieldValue `json:"custom_fields,omitempty"`
	CreatedAt       string             `json:"created_at,omitempty"`
	UpdatedAt       string             `json:"updated_at,omitempty"`
	CanBeSolvedByMe bool               `json:"can_be_solved_by_me"`
	IsPublic        bool               `json:"is_public"`
}
