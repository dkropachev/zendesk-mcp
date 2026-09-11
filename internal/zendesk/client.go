package zendesk

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTimeout          = 60 * time.Second
	defaultMaxResponseBytes = int64(8 * 1024 * 1024)
	maxZendeskUploadBytes   = int64(50 * 1024 * 1024)
	defaultUserAgent        = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36"
	configVersion           = 1
)

var ErrBaseURLRequired = errors.New("Zendesk base URL is required")

type Config struct {
	Version                       int           `json:"version,omitempty"`
	BaseURL                       string        `json:"base_url"`
	CredentialHost                string        `json:"credential_host,omitempty"`
	AuthMode                      string        `json:"auth_mode"`
	Cookie                        string        `json:"cookie,omitempty"`
	CookieFile                    string        `json:"cookie_file,omitempty"`
	HeadersFile                   string        `json:"headers_file,omitempty"`
	OAuthToken                    string        `json:"oauth_token,omitempty"`
	OAuthTokenFile                string        `json:"oauth_token_file,omitempty"`
	Email                         string        `json:"email,omitempty"`
	APIToken                      string        `json:"api_token,omitempty"`
	APITokenFile                  string        `json:"api_token_file,omitempty"`
	DownloadRoot                  string        `json:"download_root,omitempty"`
	UploadRoot                    string        `json:"upload_root,omitempty"`
	EnableWrite                   bool          `json:"enable_write,omitempty"`
	Timeout                       time.Duration `json:"-"`
	MaxResponseBytes              int64         `json:"max_response_bytes,omitempty"`
	MaxDownloadBytes              int64         `json:"max_download_bytes,omitempty"`
	MaxReadRetries                int           `json:"max_read_retries,omitempty"`
	TLSSkipVerify                 bool          `json:"tls_insecure_skip_verify,omitempty"`
	IntegrationAllowWrite         bool          `json:"-"`
	AllowNonZendeskHostForTesting bool          `json:"-"`
}

type fileConfig struct {
	Version          int    `json:"version,omitempty"`
	BaseURL          string `json:"base_url"`
	CredentialHost   string `json:"credential_host,omitempty"`
	AuthMode         string `json:"auth_mode"`
	Cookie           string `json:"cookie,omitempty"`
	CookieFile       string `json:"cookie_file,omitempty"`
	HeadersFile      string `json:"headers_file,omitempty"`
	OAuthToken       string `json:"oauth_token,omitempty"`
	OAuthTokenFile   string `json:"oauth_token_file,omitempty"`
	Email            string `json:"email,omitempty"`
	APIToken         string `json:"api_token,omitempty"`
	APITokenFile     string `json:"api_token_file,omitempty"`
	DownloadRoot     string `json:"download_root,omitempty"`
	UploadRoot       string `json:"upload_root,omitempty"`
	EnableWrite      bool   `json:"enable_write,omitempty"`
	Timeout          string `json:"timeout,omitempty"`
	MaxResponseBytes int64  `json:"max_response_bytes,omitempty"`
	MaxDownloadBytes int64  `json:"max_download_bytes,omitempty"`
	MaxReadRetries   *int   `json:"max_read_retries,omitempty"`
	TLSSkipVerify    bool   `json:"tls_insecure_skip_verify,omitempty"`
}

type Client struct {
	cfg            Config
	baseURL        *url.URL
	httpClient     *http.Client
	downloadClient *http.Client
	storageClient  *http.Client
	metrics        clientMetrics
	fieldCacheMu   sync.Mutex
	fieldCache     []TicketField
	fieldCacheAt   time.Time
}

type clientMetrics struct {
	requests       atomic.Uint64
	errors         atomic.Uint64
	retries        atomic.Uint64
	responseBytes  atomic.Uint64
	downloads      atomic.Uint64
	downloadBytes  atomic.Uint64
	totalLatencyNS atomic.Uint64
	status2xx      atomic.Uint64
	status3xx      atomic.Uint64
	status4xx      atomic.Uint64
	status5xx      atomic.Uint64
	rateLimited    atomic.Uint64
}

type MetricsSnapshot struct {
	Requests       uint64 `json:"requests"`
	Errors         uint64 `json:"errors"`
	Retries        uint64 `json:"retries"`
	ResponseBytes  uint64 `json:"response_bytes"`
	Downloads      uint64 `json:"downloads"`
	DownloadBytes  uint64 `json:"download_bytes"`
	TotalLatencyMS uint64 `json:"total_latency_ms"`
	Status2xx      uint64 `json:"status_2xx"`
	Status3xx      uint64 `json:"status_3xx"`
	Status4xx      uint64 `json:"status_4xx"`
	Status5xx      uint64 `json:"status_5xx"`
	RateLimited    uint64 `json:"rate_limited"`
}

type Response struct {
	StatusCode int
	URL        string
	Header     http.Header
	Body       []byte
	RateLimit  RateLimit
}

type AuthExpiredError struct {
	StatusCode int
	AuthMode   string
}

func (e AuthExpiredError) Error() string {
	switch e.AuthMode {
	case "oauth":
		return fmt.Sprintf("AUTH_EXPIRED: Zendesk OAuth token is missing, expired, or rejected (HTTP %d). Reauthorize or refresh configured token file, then retry", e.StatusCode)
	case "api_token":
		return fmt.Sprintf("AUTH_EXPIRED: Zendesk API token is missing, revoked, or rejected (HTTP %d). Replace configured token or migrate to OAuth, then retry", e.StatusCode)
	default:
		return fmt.Sprintf("AUTH_EXPIRED: Zendesk browser session is missing, expired, or rejected (HTTP %d). Run `zendesk-mcp login`, paste fresh Chrome DevTools Copy as cURL locally, then retry. Never paste cookies into chat", e.StatusCode)
	}
}

func DefaultConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "zendesk-mcp"), nil
}

func DefaultConfigFile() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func ConfigFromEnv() (Config, error) {
	path := strings.TrimSpace(os.Getenv("ZENDESK_CONFIG_FILE"))
	if path == "" {
		var err error
		path, err = DefaultConfigFile()
		if err != nil {
			return Config{}, err
		}
	}
	cfg, err := ConfigFromFile(expandHome(path))
	if err != nil {
		return Config{}, err
	}

	setString := func(env string, target *string) {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			*target = value
		}
	}
	setString("ZENDESK_BASE_URL", &cfg.BaseURL)
	setString("ZENDESK_AUTH_MODE", &cfg.AuthMode)
	setString("ZENDESK_COOKIE", &cfg.Cookie)
	setString("ZENDESK_COOKIE_FILE", &cfg.CookieFile)
	setString("ZENDESK_HEADERS_FILE", &cfg.HeadersFile)
	setString("ZENDESK_OAUTH_TOKEN", &cfg.OAuthToken)
	setString("ZENDESK_OAUTH_TOKEN_FILE", &cfg.OAuthTokenFile)
	setString("ZENDESK_EMAIL", &cfg.Email)
	setString("ZENDESK_API_TOKEN", &cfg.APIToken)
	setString("ZENDESK_API_TOKEN_FILE", &cfg.APITokenFile)
	setString("ZENDESK_DOWNLOAD_ROOT", &cfg.DownloadRoot)
	setString("ZENDESK_UPLOAD_ROOT", &cfg.UploadRoot)
	if value := strings.TrimSpace(os.Getenv("ZENDESK_TIMEOUT")); value != "" {
		cfg.Timeout, err = time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENDESK_TIMEOUT: %w", err)
		}
	}
	if err := int64Env("ZENDESK_MAX_RESPONSE_BYTES", &cfg.MaxResponseBytes); err != nil {
		return Config{}, err
	}
	if err := int64Env("ZENDESK_MAX_DOWNLOAD_BYTES", &cfg.MaxDownloadBytes); err != nil {
		return Config{}, err
	}
	if value := strings.TrimSpace(os.Getenv("ZENDESK_MAX_READ_RETRIES")); value != "" {
		cfg.MaxReadRetries, err = strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENDESK_MAX_READ_RETRIES: %w", err)
		}
	}
	if err := boolEnv("ZENDESK_TLS_INSECURE_SKIP_VERIFY", &cfg.TLSSkipVerify); err != nil {
		return Config{}, err
	}
	if err := boolEnv("ZENDESK_ENABLE_WRITE", &cfg.EnableWrite); err != nil {
		return Config{}, err
	}
	if err := boolEnv("ZENDESK_INTEGRATION_ALLOW_WRITE", &cfg.IntegrationAllowWrite); err != nil {
		return Config{}, err
	}
	if inlineEnvironmentCredentialSelected(cfg) {
		// An explicit inline credential replaces, rather than rebinds, any
		// credential file from the stored configuration.
		cfg.CredentialHost = ""
	}
	if strings.TrimSpace(cfg.BaseURL) != "" && strings.TrimSpace(cfg.CredentialHost) == "" && usesCredentialFile(cfg) {
		return Config{}, errors.New("credential files require a persistent tenant binding; run zendesk-mcp login or configure them in config.json")
	}
	return normalizeConfig(cfg)
}

func ConfigFromFile(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && os.Getenv("ZENDESK_CONFIG_FILE") == "" {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read config file: %w", err)
	}
	var file fileConfig
	if err := json.Unmarshal(data, &file); err != nil {
		return Config{}, fmt.Errorf("parse config file: %w", err)
	}
	cfg.Version = file.Version
	cfg.BaseURL = file.BaseURL
	cfg.CredentialHost = file.CredentialHost
	cfg.AuthMode = file.AuthMode
	cfg.Cookie = file.Cookie
	cfg.CookieFile = file.CookieFile
	cfg.HeadersFile = file.HeadersFile
	cfg.OAuthToken = file.OAuthToken
	cfg.OAuthTokenFile = file.OAuthTokenFile
	cfg.Email = file.Email
	cfg.APIToken = file.APIToken
	cfg.APITokenFile = file.APITokenFile
	cfg.DownloadRoot = file.DownloadRoot
	cfg.UploadRoot = file.UploadRoot
	cfg.EnableWrite = file.EnableWrite
	cfg.MaxResponseBytes = file.MaxResponseBytes
	cfg.MaxDownloadBytes = file.MaxDownloadBytes
	if file.MaxReadRetries != nil {
		cfg.MaxReadRetries = *file.MaxReadRetries
	}
	cfg.TLSSkipVerify = file.TLSSkipVerify
	if file.Timeout != "" {
		cfg.Timeout, err = time.ParseDuration(file.Timeout)
		if err != nil {
			return Config{}, fmt.Errorf("parse config timeout: %w", err)
		}
	}
	return normalizeConfig(cfg)
}

func defaultConfig() Config {
	return Config{
		Version:          configVersion,
		BaseURL:          "",
		Timeout:          defaultTimeout,
		MaxResponseBytes: defaultMaxResponseBytes,
		MaxReadRetries:   1,
	}
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.Version == 0 {
		cfg.Version = configVersion
	}
	if cfg.Version != configVersion {
		return Config{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return Config{}, fmt.Errorf("%w; set ZENDESK_BASE_URL or run zendesk-mcp login", ErrBaseURLRequired)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil {
		return Config{}, fmt.Errorf("base URL must be absolute credential-free https URL: %q", cfg.BaseURL)
	}
	baseHost := baseURL.Hostname()
	if !cfg.AllowNonZendeskHostForTesting {
		baseHost, err = canonicalDNSHostname(baseHost)
		if err != nil {
			return Config{}, fmt.Errorf("invalid base URL host: %w", err)
		}
	} else {
		baseHost = strings.ToLower(baseHost)
	}
	cfg.CredentialHost = strings.TrimSpace(cfg.CredentialHost)
	if cfg.CredentialHost != "" && !cfg.AllowNonZendeskHostForTesting {
		cfg.CredentialHost, err = canonicalDNSHostname(cfg.CredentialHost)
		if err != nil {
			return Config{}, fmt.Errorf("invalid credential host: %w", err)
		}
	} else {
		cfg.CredentialHost = strings.ToLower(cfg.CredentialHost)
	}
	if cfg.CredentialHost == "" {
		cfg.CredentialHost = baseHost
	} else if cfg.CredentialHost != baseHost {
		return Config{}, fmt.Errorf("credential host %q does not match base URL host %q", cfg.CredentialHost, baseHost)
	}
	cfg.AuthMode = strings.ToLower(strings.TrimSpace(cfg.AuthMode))
	cfg.CookieFile = expandHome(strings.TrimSpace(cfg.CookieFile))
	cfg.HeadersFile = expandHome(strings.TrimSpace(cfg.HeadersFile))
	cfg.OAuthTokenFile = expandHome(strings.TrimSpace(cfg.OAuthTokenFile))
	cfg.APITokenFile = expandHome(strings.TrimSpace(cfg.APITokenFile))
	cfg.DownloadRoot = expandHome(strings.TrimSpace(cfg.DownloadRoot))
	cfg.UploadRoot = expandHome(strings.TrimSpace(cfg.UploadRoot))
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	if cfg.MaxDownloadBytes < 0 {
		return Config{}, errors.New("max_download_bytes must be zero or positive")
	}
	if cfg.MaxReadRetries < 0 || cfg.MaxReadRetries > 3 {
		return Config{}, errors.New("max_read_retries must be between 0 and 3")
	}
	if cfg.AuthMode == "" {
		switch {
		case cfg.OAuthToken != "" || cfg.OAuthTokenFile != "":
			cfg.AuthMode = "oauth"
		case cfg.APIToken != "" || cfg.APITokenFile != "":
			cfg.AuthMode = "api_token"
		default:
			cfg.AuthMode = "browser"
		}
	}
	if cfg.AuthMode != "browser" && cfg.AuthMode != "oauth" && cfg.AuthMode != "api_token" {
		return Config{}, fmt.Errorf("unsupported auth mode %q", cfg.AuthMode)
	}
	if cfg.EnableWrite && cfg.AuthMode == "browser" {
		return Config{}, errors.New("writes with browser-cookie auth are disabled; configure OAuth or API-token auth")
	}
	return cfg, nil
}

func New(cfg Config) (*Client, error) {
	var err error
	cfg, err = normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	host := baseURL.Hostname()
	if !cfg.AllowNonZendeskHostForTesting {
		host, err = canonicalDNSHostname(host)
		if err != nil {
			return nil, fmt.Errorf("invalid base URL host: %w", err)
		}
		if !strings.HasSuffix(host, ".zendesk.com") || host == ".zendesk.com" {
			return nil, fmt.Errorf("base URL host must be a zendesk.com tenant: %q", host)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.TLSSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- explicit local test/debug option
	}
	noRedirect := func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	downloadTransport := newDownloadTransport(transport, cfg.Timeout)
	storageTransport := newDownloadTransport(transport, cfg.Timeout)
	return &Client{
		cfg:     cfg,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:       cfg.Timeout,
			Transport:     transport,
			CheckRedirect: noRedirect,
		},
		downloadClient: &http.Client{
			Transport:     downloadTransport,
			CheckRedirect: noRedirect,
		},
		storageClient: &http.Client{
			Transport:     storageTransport,
			CheckRedirect: noRedirect,
		},
	}, nil
}

func newDownloadTransport(base *http.Transport, timeout time.Duration) *http.Transport {
	transport := base.Clone()
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = timeout
	transport.ResponseHeaderTimeout = timeout
	return transport
}

func canonicalDNSHostname(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("DNS hostname is empty")
	}
	for i := 0; i < len(host); i++ {
		if host[i] > 0x7f {
			return "", fmt.Errorf("DNS hostname must contain only ASCII characters: %q", host)
		}
	}
	host = strings.ToLower(host)
	if strings.Contains(host, "%") {
		return "", fmt.Errorf("IP zone identifiers are not allowed: %q", host)
	}
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("IP literals are not allowed: %q", host)
	}
	if len(host) > 253 {
		return "", fmt.Errorf("DNS hostname is too long: %q", host)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return "", fmt.Errorf("invalid DNS label in hostname %q", host)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid DNS label in hostname %q", host)
		}
		for i := 0; i < len(label); i++ {
			character := label[i]
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", fmt.Errorf("invalid DNS label in hostname %q", host)
			}
		}
	}
	return host, nil
}

func usesCredentialFile(cfg Config) bool {
	switch selectedAuthMode(cfg) {
	case "browser":
		return strings.TrimSpace(cfg.Cookie) == "" && strings.TrimSpace(cfg.CookieFile) != ""
	case "oauth":
		return strings.TrimSpace(cfg.OAuthToken) == "" && strings.TrimSpace(cfg.OAuthTokenFile) != ""
	case "api_token":
		return strings.TrimSpace(cfg.APIToken) == "" && strings.TrimSpace(cfg.APITokenFile) != ""
	default:
		return false
	}
}

func inlineEnvironmentCredentialSelected(cfg Config) bool {
	switch selectedAuthMode(cfg) {
	case "browser":
		return strings.TrimSpace(os.Getenv("ZENDESK_COOKIE")) != ""
	case "oauth":
		return strings.TrimSpace(os.Getenv("ZENDESK_OAUTH_TOKEN")) != ""
	case "api_token":
		return strings.TrimSpace(os.Getenv("ZENDESK_API_TOKEN")) != ""
	default:
		return false
	}
}

func selectedAuthMode(cfg Config) string {
	authMode := strings.ToLower(strings.TrimSpace(cfg.AuthMode))
	if authMode != "" {
		return authMode
	}
	switch {
	case strings.TrimSpace(cfg.OAuthToken) != "" || strings.TrimSpace(cfg.OAuthTokenFile) != "":
		return "oauth"
	case strings.TrimSpace(cfg.APIToken) != "" || strings.TrimSpace(cfg.APITokenFile) != "":
		return "api_token"
	default:
		return "browser"
	}
}

func (c *Client) Config() Config {
	result := c.cfg
	result.Cookie = ""
	result.OAuthToken = ""
	result.APIToken = ""
	return result
}

func (c *Client) BaseURL() string      { return c.cfg.BaseURL }
func (c *Client) AuthMode() string     { return c.cfg.AuthMode }
func (c *Client) WriteEnabled() bool   { return c.cfg.EnableWrite }
func (c *Client) DownloadRoot() string { return c.cfg.DownloadRoot }
func (c *Client) UploadRoot() string   { return c.cfg.UploadRoot }

func (c *Client) Metrics() MetricsSnapshot {
	return MetricsSnapshot{
		Requests:       c.metrics.requests.Load(),
		Errors:         c.metrics.errors.Load(),
		Retries:        c.metrics.retries.Load(),
		ResponseBytes:  c.metrics.responseBytes.Load(),
		Downloads:      c.metrics.downloads.Load(),
		DownloadBytes:  c.metrics.downloadBytes.Load(),
		TotalLatencyMS: c.metrics.totalLatencyNS.Load() / uint64(time.Millisecond),
		Status2xx:      c.metrics.status2xx.Load(),
		Status3xx:      c.metrics.status3xx.Load(),
		Status4xx:      c.metrics.status4xx.Load(),
		Status5xx:      c.metrics.status5xx.Load(),
		RateLimited:    c.metrics.rateLimited.Load(),
	}
}

func (c *Client) Get(ctx context.Context, apiPath string, query url.Values, limitBytes int64) (*Response, error) {
	return c.DoJSON(ctx, http.MethodGet, apiPath, query, nil, limitBytes)
}

func (c *Client) DoJSON(ctx context.Context, method, apiPath string, query url.Values, requestBody any, limitBytes int64) (*Response, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		return nil, fmt.Errorf("unsupported HTTP method %q", method)
	}
	if method != http.MethodGet && !c.cfg.EnableWrite {
		return nil, errors.New("WRITE_DISABLED: set ZENDESK_ENABLE_WRITE=true with OAuth or API-token auth")
	}
	endpoint, err := c.apiEndpoint(apiPath, query)
	if err != nil {
		return nil, err
	}
	var encodedBody []byte
	if requestBody != nil {
		encodedBody, err = json.Marshal(requestBody)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
	}
	attempts := 1
	if method == http.MethodGet {
		attempts += c.cfg.MaxReadRetries
	}
	for attempt := 0; attempt < attempts; attempt++ {
		resp, err := c.doJSONOnce(ctx, method, endpoint, encodedBody, limitBytes)
		if err == nil {
			return resp, nil
		}
		var apiErr *APIError
		if method != http.MethodGet || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || attempt+1 >= attempts {
			return resp, err
		}
		wait := apiErr.RetryAfter
		if wait <= 0 {
			wait = 100 * time.Millisecond
		}
		if wait > 5*time.Second {
			return resp, err
		}
		c.metrics.retries.Add(1)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return resp, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("unreachable request state")
}

func (c *Client) doJSONOnce(ctx context.Context, method string, endpoint *url.URL, encodedBody []byte, limitBytes int64) (*Response, error) {
	started := time.Now()
	c.metrics.requests.Add(1)
	defer func() { c.metrics.totalLatencyNS.Add(uint64(time.Since(started))) }()
	var body io.Reader
	if encodedBody != nil {
		body = bytes.NewReader(encodedBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		c.metrics.errors.Add(1)
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if encodedBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.applyAuth(req); err != nil {
		c.metrics.errors.Add(1)
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.metrics.errors.Add(1)
		return nil, err
	}
	c.recordStatus(resp.StatusCode)
	defer resp.Body.Close()
	result := &Response{
		StatusCode: resp.StatusCode,
		URL:        endpoint.Redacted(),
		Header:     resp.Header.Clone(),
		RateLimit:  parseRateLimit(resp.Header),
	}
	limitBytes = c.responseLimit(limitBytes)
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, limitBytes+1))
	result.Body = responseBody
	if err != nil {
		c.metrics.errors.Add(1)
		return result, err
	}
	c.metrics.responseBytes.Add(uint64(len(responseBody)))
	if int64(len(responseBody)) > limitBytes {
		c.metrics.errors.Add(1)
		return result, fmt.Errorf("RESPONSE_TOO_LARGE: response exceeded %d bytes; narrow query or reduce page_size", limitBytes)
	}
	if looksLikeExpiredAuth(result) {
		c.metrics.errors.Add(1)
		return result, AuthExpiredError{StatusCode: resp.StatusCode, AuthMode: c.cfg.AuthMode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.metrics.errors.Add(1)
		return result, newAPIError(resp.StatusCode, responseBody, resp.Header)
	}
	if len(responseBody) > 0 && !json.Valid(responseBody) {
		c.metrics.errors.Add(1)
		return result, errors.New("Zendesk returned non-JSON response")
	}
	return result, nil
}

func (c *Client) recordStatus(status int) {
	switch status / 100 {
	case 2:
		c.metrics.status2xx.Add(1)
	case 3:
		c.metrics.status3xx.Add(1)
	case 4:
		c.metrics.status4xx.Add(1)
	case 5:
		c.metrics.status5xx.Add(1)
	}
	if status == http.StatusTooManyRequests {
		c.metrics.rateLimited.Add(1)
	}
}

func (c *Client) apiEndpoint(apiPath string, query url.Values) (*url.URL, error) {
	if !strings.HasPrefix(apiPath, "/api/v2/") || strings.Contains(apiPath, "..") || strings.ContainsAny(apiPath, "?#") {
		return nil, fmt.Errorf("invalid Zendesk API path %q", apiPath)
	}
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + apiPath
	endpoint.RawQuery = query.Encode()
	return &endpoint, nil
}

func (c *Client) responseLimit(requested int64) int64 {
	if requested <= 0 || requested > c.cfg.MaxResponseBytes {
		return c.cfg.MaxResponseBytes
	}
	return requested
}

func (c *Client) applyAuth(req *http.Request) error {
	switch c.cfg.AuthMode {
	case "browser":
		cookie, err := secretValue(c.cfg.Cookie, c.cfg.CookieFile)
		if err != nil {
			return fmt.Errorf("read browser cookie: %w", err)
		}
		if cookie == "" {
			return AuthExpiredError{StatusCode: 0, AuthMode: c.cfg.AuthMode}
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("User-Agent", defaultUserAgent)
		if c.cfg.HeadersFile != "" {
			data, err := os.ReadFile(c.cfg.HeadersFile)
			if err != nil {
				return fmt.Errorf("read headers file: %w", err)
			}
			var headers map[string]string
			if err := json.Unmarshal(data, &headers); err != nil {
				return fmt.Errorf("parse headers file: %w", err)
			}
			for name, value := range headers {
				if allowedBrowserHeader(name) {
					req.Header.Set(name, value)
				}
			}
		}
	case "oauth":
		token, err := secretValue(c.cfg.OAuthToken, c.cfg.OAuthTokenFile)
		if err != nil {
			return fmt.Errorf("read OAuth token: %w", err)
		}
		if token == "" {
			return errors.New("OAuth token is missing")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	case "api_token":
		token, err := secretValue(c.cfg.APIToken, c.cfg.APITokenFile)
		if err != nil {
			return fmt.Errorf("read API token: %w", err)
		}
		if token == "" || strings.TrimSpace(c.cfg.Email) == "" {
			return errors.New("API token auth requires email and token")
		}
		credentials := strings.TrimSpace(c.cfg.Email) + "/token:" + token
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	return nil
}

func allowedBrowserHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "User-Agent", "Accept-Language", "Referer":
		return true
	default:
		return false
	}
}

func secretValue(inline, path string) (string, error) {
	if strings.TrimSpace(inline) != "" {
		return strings.TrimSpace(inline), nil
	}
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func looksLikeExpiredAuth(resp *Response) bool {
	if resp.StatusCode == http.StatusUnauthorized {
		return true
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return true
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	body := strings.ToLower(string(resp.Body))
	return strings.Contains(contentType, "text/html") || strings.Contains(body, "cloudflare") || strings.Contains(body, "okta.com") || strings.Contains(body, "sign in to zendesk") || (resp.StatusCode == http.StatusForbidden && strings.Contains(body, "not authenticated"))
}

func int64Env(name string, target *int64) error {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		*target = parsed
	}
	return nil
}

func boolEnv(name string, target *bool) error {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		*target = parsed
	}
	return nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
