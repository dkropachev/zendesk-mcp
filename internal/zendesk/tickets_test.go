package zendesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
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

func TestUsersBatchesAtOneHundredAndPreservesOrder(t *testing.T) {
	var batchSizes []int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := strings.Split(r.URL.Query().Get("ids"), ",")
		batchSizes = append(batchSizes, len(ids))
		users := make([]User, 0, len(ids))
		for _, raw := range ids {
			id, _ := strconv.ParseInt(raw, 10, 64)
			users = append(users, User{ID: id, Name: "user-" + raw})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
	}))
	defer server.Close()
	client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "fake", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	ids := make([]int64, 205)
	for index := range ids {
		ids[index] = int64(205 - index)
	}
	users, err := client.Users(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(batchSizes) != "[100 100 5]" {
		t.Fatalf("batch sizes=%v", batchSizes)
	}
	if len(users) != 205 || users[0].ID != 205 || users[204].ID != 1 {
		t.Fatalf("order/count wrong: first=%d last=%d count=%d", users[0].ID, users[204].ID, len(users))
	}
}

func TestValidateAssigneeGroup(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"group_memberships": []any{map[string]any{"group_id": 3}}})
	}))
	defer server.Close()
	client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	if err := client.ValidateAssigneeGroup(context.Background(), 2, 3); err != nil {
		t.Fatal(err)
	}
	if err := client.ValidateAssigneeGroup(context.Background(), 2, 4); err == nil {
		t.Fatal("invalid membership accepted")
	}
}
