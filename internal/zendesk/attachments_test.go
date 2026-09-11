package zendesk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

type generatedReader struct {
	remaining  int64
	maxRequest int
}

type countingReadCloser struct {
	io.ReadCloser
	closes atomic.Int32
}

func (r *countingReadCloser) Close() error {
	r.closes.Add(1)
	return r.ReadCloser.Close()
}

func (r *generatedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for index := range p[:n] {
		p[index] = 'x'
	}
	r.remaining -= int64(n)
	return n, nil
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

func TestWriteDownloadDoesNotRemoveExistingDestination(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "raced")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := writeDownload(strings.NewReader("replacement"), target, 0); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error=%v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("destination=%q", data)
	}
}

func TestWriteDownloadOptionalLimitChecksStreamedBytes(t *testing.T) {
	root := t.TempDir()
	exactTarget := filepath.Join(root, "exact")
	result, err := writeDownload(strings.NewReader("12345"), exactTarget, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != 5 {
		t.Fatalf("bytes=%d", result.Bytes)
	}

	oversizeTarget := filepath.Join(root, "oversize")
	if _, err := writeDownload(strings.NewReader("123456"), oversizeTarget, 5); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(oversizeTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversize target remains: %v", err)
	}

	truncatedTarget := filepath.Join(root, "truncated")
	if _, err := writeDownload(&failingReader{}, truncatedTarget, int64(len("partial"))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated stream error=%v", err)
	}
	if _, err := os.Stat(truncatedTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("truncated target remains: %v", err)
	}
}

func TestWriteDownloadStreamsWithoutBuiltInLimit(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "large")
	size := maxZendeskUploadBytes + 1
	source := &generatedReader{remaining: size}

	result, err := writeDownload(source, target, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != size {
		t.Fatalf("bytes=%d want=%d", result.Bytes, size)
	}
	if len(result.SHA256) != sha256.Size*2 {
		t.Fatalf("sha256=%q", result.SHA256)
	}
	if source.maxRequest > 1024*1024 {
		t.Fatalf("largest source read=%d; stream buffered too much", source.maxRequest)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size || info.Mode().Perm() != 0600 {
		t.Fatalf("size=%d mode=%v", info.Size(), info.Mode().Perm())
	}
}

func TestEffectiveDownloadLimit(t *testing.T) {
	tests := []struct {
		name       string
		requested  int64
		configured int64
		want       int64
	}{
		{name: "unlimited", want: 0},
		{name: "request limit", requested: 20, want: 20},
		{name: "server limit", configured: 30, want: 30},
		{name: "request lower", requested: 20, configured: 30, want: 20},
		{name: "server lower", requested: 40, configured: 30, want: 30},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveDownloadLimit(test.requested, test.configured); got != test.want {
				t.Fatalf("limit=%d want=%d", got, test.want)
			}
		})
	}
}

func TestIdleTimeoutReadCloserStopsStalledStream(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &countingReadCloser{ReadCloser: reader}
	stream := &idleTimeoutReadCloser{body: body, timeout: 10 * time.Millisecond}

	_, err := stream.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("error=%v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := body.closes.Load(); got != 1 {
		t.Fatalf("body closes=%d", got)
	}
}

func TestDownloadRequestBoundsPreHeaderPhase(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/attachment", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	resp, cancel, err := doDownloadRequest(context.Background(), client, req, 10*time.Millisecond)
	if resp != nil || cancel != nil || err == nil || !strings.Contains(err.Error(), "response headers exceeded") {
		t.Fatalf("response=%v cancel=%v error=%v", resp, cancel != nil, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("pre-header timeout took %v", elapsed)
	}
}

func TestDownloadRequestStopsHeaderTimerForBodyStreaming(t *testing.T) {
	var requestContext context.Context
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestContext = req.Context()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("body")),
			Request:    req,
		}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/attachment", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, cancel, err := doDownloadRequest(context.Background(), client, req, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	defer cancel()
	time.Sleep(30 * time.Millisecond)
	select {
	case <-requestContext.Done():
		t.Fatalf("body context canceled after headers: %v", requestContext.Err())
	default:
	}
}

func serverURL(r *http.Request) string {
	return "https://" + r.Host
}
