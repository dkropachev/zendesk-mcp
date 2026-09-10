package zendesk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadRetries429ButWriteDoesNot(t *testing.T) {
	var getCalls atomic.Int32
	var putCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "0")
		if r.Method == http.MethodGet {
			if getCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		putCalls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "secret", EnableWrite: true, TLSSkipVerify: true, MaxReadRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 1024); err != nil {
		t.Fatal(err)
	}
	if getCalls.Load() != 2 || client.Metrics().Retries != 1 || client.Metrics().RateLimited != 1 || client.Metrics().Status4xx != 1 || client.Metrics().Status2xx != 1 {
		t.Fatalf("get calls=%d metrics=%+v", getCalls.Load(), client.Metrics())
	}
	_, err = client.DoJSON(context.Background(), http.MethodPut, "/api/v2/tickets/1.json", nil, map[string]any{"ticket": map[string]any{"status": "open"}}, 1024)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || putCalls.Load() != 1 {
		t.Fatalf("write err=%v calls=%d", err, putCalls.Load())
	}
}

func TestAPIErrorRedactsURL(t *testing.T) {
	err := newAPIError(422, []byte(`{"description":"bad https://tenant.zendesk.com/attachments/token/secret?x=y"}`), http.Header{})
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "[URL redacted]") {
		t.Fatalf("error not sanitized: %v", err)
	}
}

func TestWriteDisabledAndBrowserWriteRejected(t *testing.T) {
	client, err := New(Config{BaseURL: "https://example.zendesk.com", AuthMode: "oauth", OAuthToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DoJSON(context.Background(), http.MethodPost, "/api/v2/tickets.json", nil, map[string]any{}, 1024); err == nil || !strings.Contains(err.Error(), "WRITE_DISABLED") {
		t.Fatalf("write disabled error=%v", err)
	}
	if _, err := New(Config{BaseURL: "https://example.zendesk.com", AuthMode: "browser", Cookie: "x=y", EnableWrite: true}); err == nil {
		t.Fatal("browser write configuration accepted")
	}
}

func TestHTTPErrorClassesAreTypedAndSanitized(t *testing.T) {
	for _, status := range []int{400, 403, 404, 409, 422, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"expected_test_error"}`))
			}))
			defer server.Close()
			client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", TLSSkipVerify: true})
			_, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 1024)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
				t.Fatalf("status=%d err=%T %v", status, err, err)
			}
			if strings.Contains(err.Error(), server.URL) {
				t.Fatalf("URL leaked: %v", err)
			}
		})
	}
}

func TestNoContentSuccess(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	client, _ := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "x", EnableWrite: true, TLSSkipVerify: true})
	if _, err := client.DoJSON(context.Background(), http.MethodDelete, "/api/v2/uploads/token.json", nil, nil, 1024); err != nil {
		t.Fatal(err)
	}
}
