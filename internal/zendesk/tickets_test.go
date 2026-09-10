package zendesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTicketFieldsPaginatesAndCaches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			fmt.Fprint(w, `{"ticket_fields":[{"id":1,"title":"One"}],"meta":{"has_more":true,"after_cursor":"next"}}`)
			return
		}
		if r.URL.Query().Get("page[after]") != "next" {
			t.Errorf("cursor=%q", r.URL.Query().Get("page[after]"))
		}
		fmt.Fprint(w, `{"ticket_fields":[{"id":2,"title":"Two"}],"meta":{"has_more":false}}`)
	}))
	defer server.Close()
	client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", TLSSkipVerify: true})
	fields, err := client.TicketFields(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("fields=%v", fields)
	}
	fields, err = client.TicketFields(context.Background())
	if err != nil || len(fields) != 2 || calls.Load() != 2 {
		t.Fatalf("cache fields=%v calls=%d err=%v", fields, calls.Load(), err)
	}
}

func TestValidateAssigneeGroup(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"group_memberships": []any{map[string]any{"group_id": 3}}})
	}))
	defer server.Close()
	client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", TLSSkipVerify: true})
	if err := client.ValidateAssigneeGroup(context.Background(), 2, 3); err != nil {
		t.Fatal(err)
	}
	if err := client.ValidateAssigneeGroup(context.Background(), 2, 4); err == nil {
		t.Fatal("invalid membership accepted")
	}
}
