package zendesk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Attachment struct {
	ID                    int64        `json:"id"`
	FileName              string       `json:"file_name"`
	ContentType           string       `json:"content_type"`
	ContentURL            string       `json:"content_url,omitempty"`
	MappedContentURL      string       `json:"mapped_content_url,omitempty"`
	Size                  int64        `json:"size"`
	Deleted               bool         `json:"deleted"`
	Inline                bool         `json:"inline"`
	MalwareScanResult     string       `json:"malware_scan_result,omitempty"`
	MalwareAccessOverride bool         `json:"malware_access_override"`
	Thumbnails            []Attachment `json:"thumbnails,omitempty"`
}

type DownloadResult struct {
	AttachmentID int64  `json:"attachment_id"`
	Path         string `json:"path"`
	FileName     string `json:"file_name"`
	ContentType  string `json:"content_type"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

func (c *Client) GetAttachment(ctx context.Context, ticketID, commentID, attachmentID int64) (*Attachment, error) {
	if ticketID <= 0 || commentID <= 0 || attachmentID <= 0 {
		return nil, errors.New("ticket_id, comment_id, and attachment_id must be positive")
	}
	commentPath := "/api/v2/tickets/" + strconv.FormatInt(ticketID, 10) + "/comments.json"
	after := ""
	for page := 0; page < 100; page++ {
		query := url.Values{"page[size]": {"100"}, "include_inline_images": {"true"}}
		if after != "" {
			query.Set("page[after]", after)
		}
		resp, err := c.Get(ctx, commentPath, query, c.cfg.MaxResponseBytes)
		if err != nil {
			return nil, err
		}
		var result struct {
			Comments []struct {
				ID          int64        `json:"id"`
				Attachments []Attachment `json:"attachments"`
			} `json:"comments"`
			Meta PageMeta `json:"meta"`
		}
		if err := json.Unmarshal(resp.Body, &result); err != nil {
			return nil, fmt.Errorf("parse ticket comments: %w", err)
		}
		for _, comment := range result.Comments {
			if comment.ID != commentID {
				continue
			}
			for _, attachment := range comment.Attachments {
				if attachment.ID == attachmentID {
					return &attachment, nil
				}
			}
			return nil, errors.New("attachment does not belong to specified accessible ticket comment")
		}
		if !result.Meta.HasMore || result.Meta.AfterCursor == "" {
			break
		}
		after = result.Meta.AfterCursor
	}
	return nil, errors.New("specified accessible ticket comment was not found")
}

func (c *Client) DownloadAttachment(ctx context.Context, attachment Attachment, destination string, maxBytes int64) (*DownloadResult, error) {
	if attachment.ID <= 0 {
		return nil, errors.New("attachment id must be positive")
	}
	if attachment.Deleted {
		return nil, errors.New("attachment was deleted")
	}
	if attachment.MalwareScanResult != "malware_not_found" {
		return nil, fmt.Errorf("attachment download blocked: malware_scan_result=%q", attachment.MalwareScanResult)
	}
	if strings.TrimSpace(c.cfg.DownloadRoot) == "" {
		return nil, errors.New("attachment download disabled: configure ZENDESK_DOWNLOAD_ROOT")
	}
	maxBytes = effectiveDownloadLimit(maxBytes, c.cfg.MaxDownloadBytes)
	target, err := resolveDownloadTarget(c.cfg.DownloadRoot, destination)
	if err != nil {
		return nil, err
	}
	source, err := c.validateAttachmentSource(attachment.ContentURL)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	c.metrics.requests.Add(1)
	firstReq, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return nil, err
	}
	firstReq.Header.Set("Accept", "application/octet-stream")
	if err := c.applyAuth(firstReq); err != nil {
		return nil, err
	}
	firstResp, firstCancel, err := doDownloadRequest(ctx, c.downloadClient, firstReq, c.cfg.Timeout)
	if err != nil {
		c.metrics.errors.Add(1)
		return nil, err
	}
	c.recordStatus(firstResp.StatusCode)
	var contentResp *http.Response
	var contentCancel context.CancelFunc
	switch {
	case firstResp.StatusCode >= 300 && firstResp.StatusCode < 400:
		_ = firstResp.Body.Close()
		firstCancel()
		location := strings.TrimSpace(firstResp.Header.Get("Location"))
		storageURL, err := source.Parse(location)
		if err != nil {
			c.metrics.errors.Add(1)
			return nil, errors.New("invalid attachment redirect")
		}
		if err := validateStorageURL(storageURL); err != nil {
			c.metrics.errors.Add(1)
			return nil, err
		}
		storageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, storageURL.String(), nil)
		if err != nil {
			return nil, err
		}
		storageReq.Header.Set("User-Agent", "zendesk-mcp/0.2")
		c.metrics.requests.Add(1)
		contentResp, contentCancel, err = doDownloadRequest(ctx, c.storageClient, storageReq, c.cfg.Timeout)
		if err != nil {
			c.metrics.errors.Add(1)
			return nil, err
		}
		c.recordStatus(contentResp.StatusCode)
	case firstResp.StatusCode >= 200 && firstResp.StatusCode < 300:
		contentResp = firstResp
		contentCancel = firstCancel
	default:
		_ = firstResp.Body.Close()
		firstCancel()
		c.metrics.errors.Add(1)
		if firstResp.StatusCode == http.StatusUnauthorized || firstResp.StatusCode == http.StatusForbidden {
			return nil, AuthExpiredError{StatusCode: firstResp.StatusCode, AuthMode: c.cfg.AuthMode}
		}
		return nil, fmt.Errorf("attachment endpoint HTTP %d", firstResp.StatusCode)
	}
	body := &idleTimeoutReadCloser{body: contentResp.Body, timeout: c.cfg.Timeout}
	defer func() {
		_ = body.Close()
		contentCancel()
	}()
	if contentResp.StatusCode < 200 || contentResp.StatusCode >= 300 {
		c.metrics.errors.Add(1)
		return nil, fmt.Errorf("attachment storage HTTP %d", contentResp.StatusCode)
	}
	if maxBytes > 0 && contentResp.ContentLength > maxBytes {
		c.metrics.errors.Add(1)
		return nil, fmt.Errorf("attachment exceeds %d-byte download limit", maxBytes)
	}
	result, err := writeDownload(body, target, maxBytes)
	if err != nil {
		c.metrics.errors.Add(1)
		return nil, err
	}
	result.AttachmentID = attachment.ID
	result.FileName = attachment.FileName
	result.ContentType = attachment.ContentType
	if value := strings.TrimSpace(contentResp.Header.Get("Content-Type")); value != "" {
		result.ContentType = value
	}
	c.metrics.downloads.Add(1)
	c.metrics.downloadBytes.Add(uint64(result.Bytes))
	c.metrics.responseBytes.Add(uint64(result.Bytes))
	c.metrics.totalLatencyNS.Add(uint64(time.Since(started)))
	return result, nil
}

func doDownloadRequest(parent context.Context, client *http.Client, req *http.Request, timeout time.Duration) (*http.Response, context.CancelFunc, error) {
	requestCtx, cancel := context.WithCancel(parent)
	timedOut := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		cancel()
		close(timedOut)
	})
	resp, err := client.Do(req.Clone(requestCtx))
	if !timer.Stop() {
		<-timedOut
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancel()
		cause := parent.Err()
		if cause == nil {
			cause = context.DeadlineExceeded
		}
		return nil, nil, fmt.Errorf("attachment response headers exceeded %s: %w", timeout, cause)
	}
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

type idleTimeoutReadCloser struct {
	body      io.ReadCloser
	timeout   time.Duration
	closeOnce sync.Once
	closeErr  error
}

func (r *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	if r.timeout <= 0 {
		return r.body.Read(p)
	}
	fired := make(chan struct{})
	timer := time.AfterFunc(r.timeout, func() {
		_ = r.Close()
		close(fired)
	})
	n, err := r.body.Read(p)
	if !timer.Stop() {
		<-fired
	}
	select {
	case <-fired:
		return n, fmt.Errorf("attachment download stalled for %s without receiving data", r.timeout)
	default:
		return n, err
	}
}

func (r *idleTimeoutReadCloser) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.body.Close()
	})
	return r.closeErr
}

func effectiveDownloadLimit(requested, configured int64) int64 {
	if requested <= 0 {
		return configured
	}
	if configured <= 0 || requested < configured {
		return requested
	}
	return configured
}

func (c *Client) validateAttachmentSource(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return nil, errors.New("invalid attachment content URL")
	}
	if !strings.EqualFold(parsed.Host, c.baseURL.Host) || !strings.HasPrefix(parsed.EscapedPath(), "/attachments/") {
		return nil, errors.New("attachment content URL is outside configured Zendesk tenant")
	}
	return parsed, nil
}

func validateStorageURL(parsed *url.URL) error {
	if parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("attachment redirect must be credential-free HTTPS URL")
	}
	host, err := canonicalDNSHostname(parsed.Hostname())
	if err != nil {
		return errors.New("attachment redirect host is not approved Zendesk storage")
	}
	if !strings.HasSuffix(host, ".zdusercontent.com") || host == ".zdusercontent.com" {
		return errors.New("attachment redirect host is not approved Zendesk storage")
	}
	return nil
}

func resolveDownloadTarget(root, destination string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", errors.New("destination is required")
	}
	if filepath.IsAbs(destination) {
		return "", errors.New("destination must be relative to configured download root")
	}
	clean := filepath.Clean(destination)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("destination escapes configured download root")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve download root: %w", err)
	}
	rootInfo, err := os.Stat(rootReal)
	if err != nil || !rootInfo.IsDir() {
		return "", errors.New("configured download root is not a directory")
	}
	target := filepath.Join(rootReal, clean)
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return "", fmt.Errorf("resolve destination directory: %w", err)
	}
	if !pathWithin(rootReal, parentReal) {
		return "", errors.New("destination directory escapes configured download root through symlink")
	}
	target = filepath.Join(parentReal, filepath.Base(target))
	if _, err := os.Lstat(target); err == nil {
		return "", errors.New("destination already exists; overwrite is forbidden")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return target, nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func writeDownload(source io.Reader, target string, maxBytes int64) (*DownloadResult, error) {
	temp, err := os.CreateTemp(filepath.Dir(target), ".zendesk-download-*")
	if err != nil {
		return nil, err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0600); err != nil {
		return nil, err
	}
	hash := sha256.New()
	stream := source
	var limited *io.LimitedReader
	if maxBytes > 0 {
		limited = &io.LimitedReader{R: source, N: maxBytes}
		stream = limited
	}
	written, err := io.Copy(io.MultiWriter(temp, hash), stream)
	if err != nil {
		return nil, err
	}
	if limited != nil && limited.N == 0 {
		var extra [1]byte
		extraBytes, extraErr := io.ReadFull(source, extra[:])
		if extraBytes > 0 {
			return nil, fmt.Errorf("attachment exceeded %d-byte download limit", maxBytes)
		}
		if extraErr != nil && !errors.Is(extraErr, io.EOF) {
			return nil, extraErr
		}
	}
	if err := temp.Sync(); err != nil {
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tempPath, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errors.New("destination already exists; overwrite is forbidden")
		}
		return nil, fmt.Errorf("publish downloaded file: %w", err)
	}
	return &DownloadResult{
		Path:   target,
		Bytes:  written,
		SHA256: hex.EncodeToString(hash.Sum(nil)),
	}, nil
}
