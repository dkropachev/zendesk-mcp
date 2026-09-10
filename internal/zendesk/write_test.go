package zendesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadFileConfinedAndTyped(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	var gotType, gotBody string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		data, _ := os.ReadFile(filepath.Join(root, "evidence.txt"))
		received := make([]byte, len(data))
		_, _ = r.Body.Read(received)
		gotBody = string(received)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"upload":{"token":"opaque","attachment":{"id":9,"file_name":"evidence.txt"}}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, UploadRoot: root, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.UploadFile(context.Background(), "evidence.txt")
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "opaque" || gotBody != "evidence" || !strings.HasPrefix(gotType, "text/plain") {
		t.Fatalf("result=%+v type=%q body=%q", result, gotType, gotBody)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ValidateUploadPath("escape"); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestTypedCreateTicketBody(t *testing.T) {
	var captured map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ticket":{"id":1}}`)
	}))
	defer server.Close()
	client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	_, err := client.CreateTicket(context.Background(), TicketCreate{Subject: "s", Comment: CommentWrite{Body: "b", Public: true}})
	if err != nil {
		t.Fatal(err)
	}
	ticket := captured["ticket"].(map[string]any)
	if ticket["subject"] != "s" || ticket["comment"].(map[string]any)["public"] != true {
		t.Fatalf("body=%#v", captured)
	}
}
