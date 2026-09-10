package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

func TestMCPListAndCallTicketComments(t *testing.T) {
	var gotPath, gotQuery string
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/users/me.json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":{"id":1,"role":"agent"}}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comments":[{"id":9}],"meta":{"has_more":false}}`))
	}))
	defer httpServer.Close()
	client, err := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "secret", TLSSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(client)

	list, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if !ok {
		t.Fatal("tools/list returned no response")
	}
	var listResult struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list, &listResult); err != nil {
		t.Fatal(err)
	}
	if len(listResult.Result.Tools) < 18 {
		t.Fatalf("tool count = %d, want at least 18", len(listResult.Result.Tools))
	}

	call, ok := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"zendesk_list_ticket_comments","arguments":{"ticket_id":12345,"page_size":1,"after_cursor":"next"}}}`))
	if !ok {
		t.Fatal("tools/call returned no response")
	}
	if gotPath != "/api/v2/tickets/12345/comments.json" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "page%5Bafter%5D=next") || !strings.Contains(gotQuery, "page%5Bsize%5D=1") {
		t.Fatalf("query = %q", gotQuery)
	}
	var callResult struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(call, &callResult); err != nil {
		t.Fatal(err)
	}
	if len(callResult.Result.Content) != 1 || !strings.Contains(callResult.Result.Content[0].Text, `"id":9`) {
		t.Fatalf("call response = %s", call)
	}
}
