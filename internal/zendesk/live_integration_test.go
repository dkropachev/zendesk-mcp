package zendesk

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestLiveReadIntegration(t *testing.T) {
	if os.Getenv("ZENDESK_INTEGRATION") != "1" {
		t.Skip("set ZENDESK_INTEGRATION=1 for live read acceptance")
	}
	rawID := os.Getenv("ZENDESK_INTEGRATION_TICKET_ID")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("ZENDESK_INTEGRATION_TICKET_ID must be positive")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	user, err := client.CurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user.ID <= 0 {
		t.Fatal("missing current user")
	}
	ticket, err := client.Ticket(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.ID != id {
		t.Fatalf("ticket id=%d", ticket.ID)
	}
	comments, _, err := client.TicketComments(context.Background(), id, 1, "desc", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live read accepted tenant=%s role=%s ticket=%d comments_returned=%d", client.BaseURL(), user.Role, id, len(comments))
}

func TestLiveSandboxWriteIntegration(t *testing.T) {
	if os.Getenv("ZENDESK_INTEGRATION") != "1" || os.Getenv("ZENDESK_INTEGRATION_ALLOW_WRITE") != "1" {
		t.Skip("requires explicit live sandbox write gates")
	}
	base := strings.TrimRight(strings.ToLower(os.Getenv("ZENDESK_BASE_URL")), "/")
	ack := strings.TrimRight(strings.ToLower(os.Getenv("ZENDESK_INTEGRATION_SANDBOX_ACK")), "/")
	if base == "" || ack != base {
		t.Fatal("ZENDESK_INTEGRATION_SANDBOX_ACK must exactly match approved sandbox base URL")
	}
	requesterID, err := strconv.ParseInt(os.Getenv("ZENDESK_INTEGRATION_REQUESTER_ID"), 10, 64)
	if err != nil || requesterID <= 0 {
		t.Fatal("ZENDESK_INTEGRATION_REQUESTER_ID must identify approved test requester")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableWrite {
		t.Fatal("ZENDESK_ENABLE_WRITE=true required")
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreateTicket(context.Background(), TicketCreate{Subject: "zendesk-mcp integration test", Comment: CommentWrite{Body: "Automated sandbox acceptance test. Safe to ignore.", Public: false}, RequesterID: requesterID, Tags: []string{"mcp-integration-test"}})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Ticket Ticket `json:"ticket"`
		Audit  struct {
			ID int64 `json:"id"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Ticket.ID <= 0 || result.Audit.ID <= 0 {
		t.Fatalf("missing write evidence: %s", resp.Body)
	}
	t.Logf("sandbox write accepted ticket_id=%d audit_id=%d", result.Ticket.ID, result.Audit.ID)
}
