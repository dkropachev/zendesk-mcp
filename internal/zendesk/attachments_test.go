package zendesk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{ sent bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		copy(p, "partial")
		return len("partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestValidateStorageURLRejectsIPLiteralWithMisleadingZone(t *testing.T) {
	storageURL, err := url.Parse("https://[::ffff:127.0.0.1%25x.zdusercontent.com]/signed/redacted")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStorageURL(storageURL); err == nil {
		t.Fatal("IP literal with misleading zone passed the storage-host allowlist")
	}
}

func TestGetAndDownloadAttachmentStripsCredentials(t *testing.T) {
	root := t.TempDir()
	var storageRequest *http.Request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/tickets/7/comments.json":
			w.Header().Set("Content-Type", "application/json")
			payload := map[string]any{"comments": []any{map[string]any{
				"id": 8, "attachments": []any{map[string]any{
					"id": 9, "file_name": "evidence.txt", "content_type": "text/plain", "size": 8,
					"content_url":         serverURL(r) + "/attachments/token/redacted/?name=evidence.txt",
					"malware_scan_result": "malware_not_found",
				}}}}, "meta": map[string]any{"has_more": false}}
			_ = json.NewEncoder(w).Encode(payload)
		case "/attachments/token/redacted/":
			if r.Header.Get("Cookie") != "session=secret" {
				t.Errorf("first-party cookie = %q", r.Header.Get("Cookie"))
			}
			http.Redirect(w, r, "https://p29.zdusercontent.com/signed/redacted", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, AuthMode: "browser", Cookie: "session=secret", DownloadRoot: root, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true})
	if err != nil {
		t.Fatal(err)
	}
	client.storageClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		storageRequest = req.Clone(req.Context())
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": {"text/plain"}, "Content-Length": {"8"}},
			Body:          io.NopCloser(strings.NewReader("evidence")),
			ContentLength: 8,
			Request:       req,
		}, nil
	})

	attachment, err := client.GetAttachment(context.Background(), 7, 8, 9)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DownloadAttachment(context.Background(), *attachment, "case/evidence.txt", 1024)
	if err == nil && strings.Contains(result.Path, "case/evidence.txt") {
		t.Fatal("download unexpectedly succeeded before destination directory exists")
	}
	if err := os.Mkdir(filepath.Join(root, "case"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err = client.DownloadAttachment(context.Background(), *attachment, "case/evidence.txt", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if storageRequest == nil {
		t.Fatal("storage request not observed")
	}
	for _, name := range []string{"Cookie", "Authorization", "Referer", "Accept-Language"} {
		if got := storageRequest.Header.Get(name); got != "" {
			t.Errorf("storage %s leaked: %q", name, got)
		}
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "evidence" || result.Bytes != 8 || len(result.SHA256) != 64 {
		t.Fatalf("result=%+v data=%q", result, data)
	}
	info, err := os.Stat(result.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("download mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestDownloadRejectsHostOversizeMalwareAndOverwrite(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachments/hostile":
			http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
		case "/attachments/large":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(bytes.Repeat([]byte("x"), 20))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "oauth", OAuthToken: "secret", DownloadRoot: root, TLSSkipVerify: true, AllowNonZendeskHostForTesting: true, MaxDownloadBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	base := Attachment{ID: 1, FileName: "x", MalwareScanResult: "malware_not_found"}
	base.ContentURL = server.URL + "/attachments/hostile"
	if _, err := client.DownloadAttachment(context.Background(), base, "hostile", 10); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("hostile redirect error=%v", err)
	}
	base.ContentURL = server.URL + "/attachments/large"
	if _, err := client.DownloadAttachment(context.Background(), base, "large", 10); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("oversize error=%v", err)
	}
	base.MalwareScanResult = "malware_found"
	if _, err := client.DownloadAttachment(context.Background(), base, "malware", 10); err == nil || !strings.Contains(err.Error(), "malware_scan_result") {
		t.Fatalf("malware error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	base.MalwareScanResult = "malware_not_found"
	if _, err := client.DownloadAttachment(context.Background(), base, "existing", 10); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite error=%v", err)
	}
}

func TestResolveDownloadTargetRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{"../x", "/tmp/x", "escape/x"} {
		if _, err := resolveDownloadTarget(root, destination); err == nil {
			t.Errorf("destination %q accepted", destination)
		}
	}
}

func TestWriteDownloadInterruptedRemovesPartialFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "partial")
	if _, err := writeDownload(&failingReader{}, target, 100); err == nil {
		t.Fatal("interrupted stream accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("partial target remains: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files remain: %v", entries)
	}
}

func serverURL(r *http.Request) string {
	return "https://" + r.Host
}
