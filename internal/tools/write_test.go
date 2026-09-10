package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

func TestPreparedStoreDigestExpiryIdentityAndSingleUse(t *testing.T) {
	store := NewPreparedStore()
	now := time.Unix(1000, 0)
	store.now = func() time.Time { return now }
	op, err := store.Prepare(operationPayload{Kind: "x", TicketID: 1}, "https://a.zendesk.com", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(op.Digest) != 64 || len(op.ID) != 32 {
		t.Fatalf("op=%+v", op)
	}
	if _, err := store.Checkout(op.ID, "wrong", op.Tenant, op.UserID, "x"); err == nil {
		t.Fatal("wrong digest accepted")
	}
	if _, err := store.Checkout(op.ID, op.Digest, "https://b.zendesk.com", op.UserID, "x"); err == nil {
		t.Fatal("wrong tenant accepted")
	}
	if _, err := store.Checkout(op.ID, op.Digest, op.Tenant, 3, "x"); err == nil {
		t.Fatal("wrong user accepted")
	}
	if _, err := store.Checkout(op.ID, op.Digest, op.Tenant, op.UserID, "wrong-kind"); err == nil {
		t.Fatal("wrong operation kind accepted")
	}
	if _, err := store.Checkout(op.ID, op.Digest, op.Tenant, op.UserID, "x"); err != nil {
		t.Fatal(err)
	}
	store.Complete(op.ID)
	if _, err := store.Checkout(op.ID, op.Digest, op.Tenant, op.UserID, "x"); err == nil {
		t.Fatal("used op accepted")
	}
	op, _ = store.Prepare(operationPayload{Kind: "x"}, "t", 1)
	now = now.Add(11 * time.Minute)
	if _, err := store.Checkout(op.ID, op.Digest, "t", 1, "x"); err == nil {
		t.Fatal("expired op accepted")
	}
}

func TestWriteToolsDefaultOff(t *testing.T) {
	client, err := zendesk.New(zendesk.Config{BaseURL: "https://example.zendesk.com", AuthMode: "oauth", OAuthToken: "x"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(client, "test")
	response, _ := server.HandleJSON(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if strings.Contains(string(response), "zendesk_create_ticket") || strings.Contains(string(response), "zendesk_add_public_reply") {
		t.Fatalf("write tool exposed: %s", response)
	}
}

func TestInternalAndPublicCommentPrepareCommit(t *testing.T) {
	var puts atomic.Int32
	var bodies []map[string]any
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case req.Method == http.MethodGet && req.URL.Path == "/api/v2/tickets/7.json":
			fmt.Fprint(w, `{"ticket":{"id":7,"group_id":3,"updated_at":"2026-01-01T00:00:00Z"}}`)
		case req.Method == http.MethodPut && req.URL.Path == "/api/v2/tickets/7.json":
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			bodies = append(bodies, body)
			puts.Add(1)
			fmt.Fprint(w, `{"ticket":{"id":7},"audit":{"id":99}}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer httpServer.Close()
	client, err := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(client, "test")
	for _, test := range []struct {
		name   string
		public bool
	}{{"zendesk_add_internal_note", false}, {"zendesk_add_public_reply", true}} {
		prepared := callTool(t, server, test.name, map[string]any{"ticket_id": 7, "body": "message"}, false)
		commitArgs := map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": true}
		callTool(t, server, test.name, commitArgs, false)
		if got := bodies[len(bodies)-1]["ticket"].(map[string]any); got["safe_update"] != true || got["updated_stamp"] != "2026-01-01T00:00:00Z" || got["comment"].(map[string]any)["public"] != test.public {
			t.Fatalf("%s body=%#v", test.name, got)
		}
		callTool(t, server, test.name, commitArgs, true)
	}
	if puts.Load() != 2 {
		t.Fatalf("puts=%d", puts.Load())
	}
}

func TestCreateRequiresExplicitVisibilityAndConfirmation(t *testing.T) {
	var posts atomic.Int32
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case "/api/v2/tickets.json":
			posts.Add(1)
			fmt.Fprint(w, `{"ticket":{"id":10},"audit":{"id":11}}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	server := NewServer(client, "test")
	callTool(t, server, "zendesk_prepare_ticket_create", map[string]any{"subject": "x", "body": "y"}, true)
	prepared := callTool(t, server, "zendesk_prepare_ticket_create", map[string]any{"subject": "x", "body": "y", "public": true}, false)
	if posts.Load() != 0 {
		t.Fatal("prepare mutated network")
	}
	callTool(t, server, "zendesk_create_ticket", map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": false}, true)
	if posts.Load() != 0 {
		t.Fatal("unconfirmed commit mutated network")
	}
	callTool(t, server, "zendesk_create_ticket", map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": true}, false)
	if posts.Load() != 1 {
		t.Fatalf("posts=%d", posts.Load())
	}
}

func TestWriteCollisionNeverRetries(t *testing.T) {
	var puts atomic.Int32
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case req.Method == http.MethodGet:
			fmt.Fprint(w, `{"ticket":{"id":7,"group_id":3,"updated_at":"2026-01-01T00:00:00Z"}}`)
		case req.Method == http.MethodPut:
			puts.Add(1)
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":"UpdateConflict"}`)
		}
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true, MaxReadRetries: 3})
	server := NewServer(client, "test")
	prepared := callTool(t, server, "zendesk_update_ticket", map[string]any{"ticket_id": 7, "status": "pending"}, false)
	callTool(t, server, "zendesk_update_ticket", map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": true}, true)
	callTool(t, server, "zendesk_update_ticket", map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": true}, true)
	if puts.Load() != 1 {
		t.Fatalf("collision write retried %d times", puts.Load())
	}
}

func TestBodylessSuccessfulWriteReportsConsumedAttempt(t *testing.T) {
	var posts atomic.Int32
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case req.Method == http.MethodPost && req.URL.Path == "/api/v2/tickets.json":
			posts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	defer httpServer.Close()
	client, err := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(client, "test")
	prepared := callTool(t, server, "zendesk_prepare_ticket_create", map[string]any{"subject": "x", "body": "y", "public": true}, false)
	commitArgs := map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": true}
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "zendesk_create_ticket", "arguments": commitArgs}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := server.HandleJSON(context.Background(), raw)
	if !ok || !strings.Contains(string(response), `"isError":true`) || !strings.Contains(string(response), "WRITE_ATTEMPT_CONSUMED") {
		t.Fatalf("response=%s, ok=%v", response, ok)
	}
	response, ok = server.HandleJSON(context.Background(), raw)
	if !ok || !strings.Contains(string(response), `"isError":true`) || !strings.Contains(string(response), "WRITE_ATTEMPT_CONSUMED") {
		t.Fatalf("replay response=%s, ok=%v", response, ok)
	}
	if posts.Load() != 1 {
		t.Fatalf("bodyless success was attempted %d times", posts.Load())
	}
}

func TestCommentBodyLimitUsesUTF8Bytes(t *testing.T) {
	if err := validateBody(strings.Repeat("a", maxCommentBodyBytes)); err != nil {
		t.Fatal(err)
	}
	if err := validateBody(strings.Repeat("a", maxCommentBodyBytes+1)); err == nil {
		t.Fatal("oversize ASCII body accepted")
	}
	if err := validateBody(strings.Repeat("é", maxCommentBodyBytes/2+1)); err == nil {
		t.Fatal("oversize multibyte body accepted")
	}
	visibility := false
	_, err := validatedTicketCreate(ticketCreateArgs{Subject: "subject", Body: strings.Repeat("a", maxCommentBodyBytes+1), Public: &visibility}, 0)
	if err == nil {
		t.Fatal("oversize initial comment accepted")
	}
}

func TestRequestsToolRejectsPrivateComment(t *testing.T) {
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"comments":[{"id":1,"public":false}],"meta":{"has_more":false}}`)
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	server := NewServer(client, "test")
	callTool(t, server, "zendesk_list_request_comments", map[string]any{"request_id": 7}, true)
}

func TestEndUserCannotPrepareAgentWrite(t *testing.T) {
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"user":{"id":8,"role":"end-user"}}`)
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	server := NewServer(client, "test")
	callTool(t, server, "zendesk_prepare_ticket_create", map[string]any{"subject": "x", "body": "y", "public": true}, true)
}

func TestInvalidRawWriteValuesRejected(t *testing.T) {
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case "/api/v2/tickets/7.json":
			fmt.Fprint(w, `{"ticket":{"id":7,"group_id":3,"updated_at":"x"}}`)
		}
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	server := NewServer(client, "test")
	callTool(t, server, "zendesk_prepare_ticket_create", map[string]any{"subject": "x", "body": "y", "public": true, "priority": "super-urgent"}, true)
	callTool(t, server, "zendesk_update_ticket", map[string]any{"ticket_id": 7, "status": "closed"}, true)
	callTool(t, server, "zendesk_update_ticket", map[string]any{"ticket_id": 7, "custom_fields": []any{map[string]any{"id": 1, "value": map[string]any{"unsafe": true}}}}, true)
}

func TestFailedAttachmentUploadCleansTokens(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var uploads, deletes atomic.Int32
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case req.Method == http.MethodGet && req.URL.Path == "/api/v2/tickets/7.json":
			fmt.Fprint(w, `{"ticket":{"id":7,"updated_at":"stamp"}}`)
		case req.Method == http.MethodPost && req.URL.Path == "/api/v2/uploads.json":
			if uploads.Add(1) == 1 {
				fmt.Fprint(w, `{"upload":{"token":"first-token","attachment":{"id":1}}}`)
			} else {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"error":"upload failed"}`)
			}
		case req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "first-token"):
			deletes.Add(1)
			w.WriteHeader(204)
		default:
			http.NotFound(w, req)
		}
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, UploadRoot: root, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	server := NewServer(client, "test")
	prepared := callTool(t, server, "zendesk_add_comment_with_attachments", map[string]any{"ticket_id": 7, "body": "evidence", "visibility": "private", "files": []string{"one.txt", "two.txt"}}, false)
	callTool(t, server, "zendesk_add_comment_with_attachments", map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": true}, true)
	if uploads.Load() != 2 || deletes.Load() != 1 {
		t.Fatalf("uploads=%d deletes=%d", uploads.Load(), deletes.Load())
	}
}

func TestUploadCleanupSurvivesCanceledRequest(t *testing.T) {
	var deletes atomic.Int32
	serverHTTP := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method=%s", r.Method)
		}
		deletes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer serverHTTP.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: serverHTTP.URL, AuthMode: "oauth", OAuthToken: "fake", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	registry := &Registry{client: client, writes: NewPreparedStore()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry.cleanupUploads(ctx, []string{"opaque-one", "opaque-two"})
	if deletes.Load() != 2 {
		t.Fatalf("deletes=%d", deletes.Load())
	}
}

func TestAmbiguousAttachmentUpdateDoesNotDeleteUploads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	putStarted := make(chan struct{})
	releasePut := make(chan struct{})
	var deletes atomic.Int32
	serverHTTP := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/api/v2/uploads.json":
			fmt.Fprint(w, `{"upload":{"token":"opaque-token","attachment":{"id":1}}}`)
		case req.Method == http.MethodPut && req.URL.Path == "/api/v2/tickets/7.json":
			close(putStarted)
			<-releasePut
			fmt.Fprint(w, `{"ticket":{"id":7},"audit":{"id":8}}`)
		case req.Method == http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	defer serverHTTP.Close()
	defer close(releasePut)
	client, err := zendesk.New(zendesk.Config{BaseURL: serverHTTP.URL, AuthMode: "oauth", OAuthToken: "fake", EnableWrite: true, UploadRoot: root, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{client: client, writes: NewPreparedStore()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, executeErr := registry.execute(ctx, operationPayload{
			Kind:     "comment_attachments",
			TicketID: 7,
			TicketUpdate: &zendesk.TicketUpdate{
				Comment: &zendesk.CommentWrite{Body: "see attachment", Public: false},
			},
			Files: []string{"evidence.txt"},
		})
		done <- executeErr
	}()
	<-putStarted
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled update unexpectedly succeeded")
	}
	if deletes.Load() != 0 {
		t.Fatalf("ambiguous update deleted %d upload token(s)", deletes.Load())
	}
}

func TestAttachmentUpdateCleanupRequiresDefinitiveRejection(t *testing.T) {
	for _, test := range []struct {
		name             string
		status           int
		responseBody     string
		maxResponseBytes int64
		wantDeletes      int32
	}{
		{name: "validation rejection", status: http.StatusUnprocessableEntity, responseBody: `{"error":"update failed"}`, wantDeletes: 1},
		{name: "oversize validation rejection", status: http.StatusUnprocessableEntity, responseBody: strings.Repeat("x", 256), maxResponseBytes: 128, wantDeletes: 1},
		{name: "ambiguous success redirect", status: http.StatusSeeOther, responseBody: `{"redirect":"ticket"}`, wantDeletes: 0},
		{name: "ambiguous server failure", status: http.StatusInternalServerError, responseBody: `{"error":"update failed"}`, wantDeletes: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			var deletes atomic.Int32
			serverHTTP := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case req.Method == http.MethodPost && req.URL.Path == "/api/v2/uploads.json":
					fmt.Fprint(w, `{"upload":{"token":"opaque-token","attachment":{"id":1}}}`)
				case req.Method == http.MethodPut && req.URL.Path == "/api/v2/tickets/7.json":
					w.WriteHeader(test.status)
					fmt.Fprint(w, test.responseBody)
				case req.Method == http.MethodDelete:
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, req)
				}
			}))
			defer serverHTTP.Close()
			client, err := zendesk.New(zendesk.Config{BaseURL: serverHTTP.URL, AuthMode: "oauth", OAuthToken: "fake", EnableWrite: true, UploadRoot: root, MaxResponseBytes: test.maxResponseBytes, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
			if err != nil {
				t.Fatal(err)
			}
			registry := &Registry{client: client, writes: NewPreparedStore()}
			_, err = registry.execute(context.Background(), operationPayload{
				Kind:     "comment_attachments",
				TicketID: 7,
				TicketUpdate: &zendesk.TicketUpdate{
					Comment: &zendesk.CommentWrite{Body: "see attachment", Public: false},
				},
				Files: []string{"evidence.txt"},
			})
			if err == nil {
				t.Fatal("failed update unexpectedly succeeded")
			}
			if deletes.Load() != test.wantDeletes {
				t.Fatalf("deleted %d upload token(s), want %d", deletes.Load(), test.wantDeletes)
			}
		})
	}
}

func TestRequestAPIFailureStatesRemainErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"anonymous_disabled", 401, `{"error":"AuthenticationRequired"}`},
		{"unverified_email", 403, `{"error":"Forbidden","description":"email identity is not verified"}`},
		{"inaccessible_request", 404, `{"error":"RecordNotFound"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverHTTP := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer serverHTTP.Close()
			client, _ := zendesk.New(zendesk.Config{BaseURL: serverHTTP.URL, AuthMode: "oauth", OAuthToken: "x", TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
			callTool(t, NewServer(client, "test"), "zendesk_get_request", map[string]any{"request_id": 7}, true)
		})
	}
}

func TestFollowupAndRequestCommitPaths(t *testing.T) {
	var followupPosts, requestPosts, requestPuts atomic.Int32
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case req.Method == http.MethodGet && req.URL.Path == "/api/v2/tickets/7.json":
			fmt.Fprint(w, `{"ticket":{"id":7,"status":"closed"}}`)
		case req.Method == http.MethodPost && req.URL.Path == "/api/v2/tickets.json":
			followupPosts.Add(1)
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["ticket"].(map[string]any)["via_followup_source_id"] != float64(7) {
				t.Errorf("followup body=%#v", body)
			}
			fmt.Fprint(w, `{"ticket":{"id":8},"audit":{"id":9}}`)
		case req.Method == http.MethodPost && req.URL.Path == "/api/v2/requests.json":
			requestPosts.Add(1)
			fmt.Fprint(w, `{"request":{"id":10}}`)
		case req.Method == http.MethodGet && req.URL.Path == "/api/v2/requests/10.json":
			fmt.Fprint(w, `{"request":{"id":10}}`)
		case req.Method == http.MethodPut && req.URL.Path == "/api/v2/requests/10.json":
			requestPuts.Add(1)
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			comment := body["request"].(map[string]any)["comment"].(map[string]any)
			if _, supplied := comment["public"]; supplied {
				t.Errorf("request comment supplied read-only public field: %#v", body)
			}
			fmt.Fprint(w, `{"request":{"id":10}}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	server := NewServer(client, "test")
	follow := callTool(t, server, "zendesk_prepare_followup_ticket", map[string]any{"source_ticket_id": 7, "body": "continued", "public": false}, false)
	callTool(t, server, "zendesk_create_followup_ticket", map[string]any{"operation_id": follow["operation_id"], "digest": follow["digest"], "confirm": true}, false)
	request := callTool(t, server, "zendesk_prepare_request", map[string]any{"subject": "help", "body": "details"}, false)
	callTool(t, server, "zendesk_create_request", map[string]any{"operation_id": request["operation_id"], "digest": request["digest"], "confirm": true}, false)
	comment := callTool(t, server, "zendesk_add_request_comment", map[string]any{"request_id": 10, "body": "more"}, false)
	callTool(t, server, "zendesk_add_request_comment", map[string]any{"operation_id": comment["operation_id"], "digest": comment["digest"], "confirm": true}, false)
	if followupPosts.Load() != 1 || requestPosts.Load() != 1 || requestPuts.Load() != 1 {
		t.Fatalf("followup=%d request_posts=%d request_puts=%d", followupPosts.Load(), requestPosts.Load(), requestPuts.Load())
	}
}

func TestAttachmentCommentSuccessConsumesOpaqueUploadToken(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	var updateBody map[string]any
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/v2/users/me.json":
			fmt.Fprint(w, `{"user":{"id":2,"role":"agent"}}`)
		case req.Method == http.MethodGet && req.URL.Path == "/api/v2/tickets/7.json":
			fmt.Fprint(w, `{"ticket":{"id":7,"updated_at":"stamp"}}`)
		case req.Method == http.MethodPost && req.URL.Path == "/api/v2/uploads.json":
			fmt.Fprint(w, `{"upload":{"token":"opaque-upload-token","attachment":{"id":1}}}`)
		case req.Method == http.MethodPut && req.URL.Path == "/api/v2/tickets/7.json":
			_ = json.NewDecoder(req.Body).Decode(&updateBody)
			fmt.Fprint(w, `{"ticket":{"id":7},"audit":{"id":8}}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer httpServer.Close()
	client, _ := zendesk.New(zendesk.Config{BaseURL: httpServer.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, UploadRoot: root, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	server := NewServer(client, "test")
	prepared := callTool(t, server, "zendesk_add_comment_with_attachments", map[string]any{"ticket_id": 7, "body": "see file", "visibility": "private", "files": []string{"evidence.txt"}}, false)
	committed := callTool(t, server, "zendesk_add_comment_with_attachments", map[string]any{"operation_id": prepared["operation_id"], "digest": prepared["digest"], "confirm": true}, false)
	comment := updateBody["ticket"].(map[string]any)["comment"].(map[string]any)
	uploads := comment["uploads"].([]any)
	if len(uploads) != 1 || uploads[0] != "opaque-upload-token" {
		t.Fatalf("update body=%#v", updateBody)
	}
	encoded, _ := json.Marshal(committed)
	if strings.Contains(string(encoded), "opaque-upload-token") {
		t.Fatal("upload token leaked to MCP response")
	}
}

func callTool(t *testing.T, server *mcp.Server, name string, args map[string]any, wantError bool) map[string]any {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
	raw, _ := json.Marshal(request)
	response, ok := server.HandleJSON(context.Background(), raw)
	if !ok {
		t.Fatal("no response")
	}
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatal(err)
	}
	if wantError {
		if envelope.Error == nil && !envelope.Result.IsError {
			t.Fatalf("%s expected error: %s", name, response)
		}
		return nil
	}
	if envelope.Error != nil {
		t.Fatalf("%s error=%s", name, envelope.Error.Message)
	}
	if envelope.Result.IsError {
		t.Fatalf("%s tool error: %s", name, response)
	}
	if len(envelope.Result.Content) == 0 {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
