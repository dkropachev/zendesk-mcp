package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

type loginInput struct {
	BaseURL    string
	AuthMode   string
	Cookie     string
	OAuthToken string
	Email      string
	APIToken   string
	Headers    map[string]string
}

var (
	urlPattern          = regexp.MustCompile(`https://[A-Za-z0-9.-]+\.zendesk\.com(?:/[^\s'"\\]*)?`)
	singleCookiePattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-b|--cookie)\s+'([^']+)'`)
	doubleCookiePattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-b|--cookie)\s+"([^"]+)"`)
	singleHeaderPattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-H|--header)\s+'([^']+)'`)
	doubleHeaderPattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-H|--header)\s+"([^"]+)"`)
	singleUserPattern   = regexp.MustCompile(`(?m)(?:^|\s)(?:-u|--user)\s+'([^']+)'`)
	doubleUserPattern   = regexp.MustCompile(`(?m)(?:^|\s)(?:-u|--user)\s+"([^"]+)"`)
)

func runLogin(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stdout)
	configDir := flags.String("config-dir", "", "directory for local auth files")
	curlFile := flags.String("curl-file", "", "file containing Chrome DevTools Copy as cURL")
	if err := flags.Parse(args); err != nil {
		return err
	}

	var data []byte
	var err error
	if strings.TrimSpace(*curlFile) != "" {
		data, err = os.ReadFile(*curlFile)
		if err != nil {
			return fmt.Errorf("read curl file: %w", err)
		}
	} else {
		fmt.Fprintln(stdout, "Paste Chrome DevTools Copy as cURL for a working Zendesk page/API request, then press Ctrl-D.")
		fmt.Fprintln(stdout, "Login extracts OAuth bearer, API-token Basic auth, or browser cookies automatically.")
		fmt.Fprintln(stdout, "Credentials stay in ~/.config/zendesk-mcp; do not paste them into chat.")
		data, err = io.ReadAll(io.LimitReader(stdin, 2*1024*1024))
		if err != nil {
			return err
		}
	}
	parsed, err := parseLoginInput(string(data))
	if err != nil {
		return err
	}

	authConfig := zendesk.Config{BaseURL: parsed.BaseURL, AuthMode: parsed.AuthMode}
	switch parsed.AuthMode {
	case "oauth":
		authConfig.OAuthToken = parsed.OAuthToken
	case "api_token":
		authConfig.Email = parsed.Email
		authConfig.APIToken = parsed.APIToken
	case "browser":
		authConfig.Cookie = parsed.Cookie
	}
	client, err := zendesk.New(authConfig)
	if err != nil {
		return err
	}
	resp, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 256*1024)
	if err != nil {
		return fmt.Errorf("%s auth validation failed; no files changed: %w", parsed.AuthMode, err)
	}
	var me struct {
		User struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(resp.Body, &me); err != nil {
		return fmt.Errorf("parse auth validation response: %w", err)
	}
	if me.User.ID == 0 {
		return errors.New("auth validation returned no user")
	}

	dir := strings.TrimSpace(*configDir)
	if dir == "" {
		dir, err = zendesk.DefaultConfigDir()
		if err != nil {
			return err
		}
	}
	paths, err := writeLoginFiles(dir, parsed)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Zendesk auth saved: mode=%s user=%d role=%s name=%q\n", parsed.AuthMode, me.User.ID, me.User.Role, me.User.Name)
	fmt.Fprintf(stdout, "config:     %s\ncredential: %s\n", paths.config, paths.credential)
	if paths.headers != "" {
		fmt.Fprintf(stdout, "headers:    %s\n", paths.headers)
	}
	fmt.Fprintln(stdout, "Next: codex mcp add zendesk -- zendesk-mcp")
	return nil
}

func parseLoginInput(input string) (loginInput, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return loginInput{}, errors.New("empty input")
	}
	urlText := urlPattern.FindString(input)
	if urlText == "" {
		return loginInput{}, errors.New("no https://*.zendesk.com URL found")
	}
	parsedURL, err := url.Parse(urlText)
	if err != nil {
		return loginInput{}, fmt.Errorf("parse Zendesk URL: %w", err)
	}
	host := strings.ToLower(parsedURL.Hostname())
	if !strings.HasSuffix(host, ".zendesk.com") || host == ".zendesk.com" {
		return loginInput{}, errors.New("URL must use a zendesk.com tenant host")
	}

	result := loginInput{
		BaseURL: "https://" + parsedURL.Host,
		Headers: map[string]string{},
	}
	if match := singleCookiePattern.FindStringSubmatch(input); len(match) == 2 {
		result.Cookie = strings.TrimSpace(match[1])
	} else if match := doubleCookiePattern.FindStringSubmatch(input); len(match) == 2 {
		result.Cookie = strings.TrimSpace(match[1])
	}

	headers := append(singleHeaderPattern.FindAllStringSubmatch(input, -1), doubleHeaderPattern.FindAllStringSubmatch(input, -1)...)
	for _, match := range headers {
		if len(match) != 2 {
			continue
		}
		name, value, ok := strings.Cut(match[1], ":")
		if !ok {
			continue
		}
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if name == "Cookie" && result.Cookie == "" {
			result.Cookie = value
		}
		if name == "Authorization" {
			if err := captureAuthorization(&result, value); err != nil {
				return loginInput{}, err
			}
		}
		switch name {
		case "User-Agent", "Accept-Language", "Referer":
			result.Headers[name] = value
		}
	}
	users := append(singleUserPattern.FindAllStringSubmatch(input, -1), doubleUserPattern.FindAllStringSubmatch(input, -1)...)
	for _, match := range users {
		if len(match) == 2 {
			if err := captureAPITokenCredentials(&result, match[1]); err != nil {
				return loginInput{}, err
			}
		}
	}
	switch {
	case result.OAuthToken != "":
		result.AuthMode = "oauth"
	case result.APIToken != "":
		result.AuthMode = "api_token"
	case result.Cookie != "":
		result.AuthMode = "browser"
	default:
		return loginInput{}, errors.New("no supported credentials found; Copy as cURL must include Authorization or Cookie data")
	}
	return result, nil
}

func captureAuthorization(result *loginInput, value string) error {
	scheme, credential, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || strings.TrimSpace(credential) == "" {
		return errors.New("Authorization header is missing credentials")
	}
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		credential = strings.TrimSpace(credential)
		if err := validateCredentialValue("OAuth token", credential); err != nil {
			return err
		}
		result.OAuthToken = credential
		return nil
	case strings.EqualFold(scheme, "Basic"):
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(credential))
		if err != nil {
			return errors.New("Authorization Basic value is not valid base64")
		}
		return captureAPITokenCredentials(result, string(decoded))
	default:
		return fmt.Errorf("unsupported Authorization scheme %q", scheme)
	}
}

func captureAPITokenCredentials(result *loginInput, credentials string) error {
	username, token, ok := strings.Cut(strings.TrimSpace(credentials), ":")
	if !ok || strings.TrimSpace(token) == "" {
		return errors.New("API-token credentials must use email/token:TOKEN format")
	}
	if !strings.HasSuffix(strings.ToLower(username), "/token") {
		return errors.New("Basic or --user credentials are not Zendesk API-token credentials")
	}
	email := strings.TrimSpace(username[:len(username)-len("/token")])
	if email == "" {
		return errors.New("API-token credentials are missing email")
	}
	if err := validateCredentialValue("email", email); err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if err := validateCredentialValue("API token", token); err != nil {
		return err
	}
	result.Email = email
	result.APIToken = token
	return nil
}

func validateCredentialValue(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%s contains whitespace or control characters", name)
	}
	return nil
}

type loginPaths struct {
	config     string
	credential string
	cookie     string
	oauthToken string
	apiToken   string
	headers    string
}

func writeLoginFiles(dir string, input loginInput) (loginPaths, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return loginPaths{}, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return loginPaths{}, fmt.Errorf("create config directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return loginPaths{}, fmt.Errorf("secure config directory: %w", err)
	}
	paths := loginPaths{
		config:     filepath.Join(dir, "config.json"),
		cookie:     filepath.Join(dir, "cookie"),
		oauthToken: filepath.Join(dir, "oauth-token"),
		apiToken:   filepath.Join(dir, "api-token"),
		headers:    filepath.Join(dir, "headers.json"),
	}
	config := map[string]any{}
	if existing, readErr := os.ReadFile(paths.config); readErr == nil {
		var previous map[string]any
		if json.Unmarshal(existing, &previous) == nil {
			for _, key := range []string{"download_root", "upload_root", "max_download_bytes", "max_read_retries", "timeout", "max_response_bytes", "tls_insecure_skip_verify"} {
				if value, ok := previous[key]; ok {
					config[key] = value
				}
			}
		}
	}
	config["version"] = 1
	config["base_url"] = input.BaseURL
	parsedBaseURL, err := url.Parse(input.BaseURL)
	if err != nil || parsedBaseURL.Hostname() == "" {
		return loginPaths{}, errors.New("invalid Zendesk base URL")
	}
	config["credential_host"] = strings.ToLower(parsedBaseURL.Hostname())
	config["auth_mode"] = input.AuthMode
	config["enable_write"] = false
	switch input.AuthMode {
	case "oauth":
		if input.OAuthToken == "" {
			return loginPaths{}, errors.New("OAuth token is empty")
		}
		paths.credential = paths.oauthToken
		config["oauth_token_file"] = paths.oauthToken
	case "api_token":
		if input.Email == "" || input.APIToken == "" {
			return loginPaths{}, errors.New("API-token email or token is empty")
		}
		paths.credential = paths.apiToken
		config["email"] = input.Email
		config["api_token_file"] = paths.apiToken
	case "browser":
		if input.Cookie == "" {
			return loginPaths{}, errors.New("browser cookie is empty")
		}
		paths.credential = paths.cookie
		config["cookie_file"] = paths.cookie
		config["headers_file"] = paths.headers
	default:
		return loginPaths{}, fmt.Errorf("unsupported auth mode %q", input.AuthMode)
	}
	if _, ok := config["timeout"]; !ok {
		config["timeout"] = "60s"
	}
	if _, ok := config["max_response_bytes"]; !ok {
		config["max_response_bytes"] = 8388608
	}
	configJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return loginPaths{}, err
	}
	switch input.AuthMode {
	case "oauth":
		if err := writeSecretFile(paths.oauthToken, []byte(input.OAuthToken+"\n")); err != nil {
			return loginPaths{}, err
		}
	case "api_token":
		if err := writeSecretFile(paths.apiToken, []byte(input.APIToken+"\n")); err != nil {
			return loginPaths{}, err
		}
	case "browser":
		headersJSON, err := json.MarshalIndent(input.Headers, "", "  ")
		if err != nil {
			return loginPaths{}, err
		}
		if err := writeSecretFile(paths.cookie, []byte(input.Cookie+"\n")); err != nil {
			return loginPaths{}, err
		}
		if err := writeSecretFile(paths.headers, append(headersJSON, '\n')); err != nil {
			return loginPaths{}, err
		}
	}
	if err := writeSecretFile(paths.config, append(configJSON, '\n')); err != nil {
		return loginPaths{}, err
	}
	return paths, nil
}

func writeSecretFile(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".zendesk-mcp-*")
	if err != nil {
		return fmt.Errorf("create temporary auth file: %w", err)
	}
	tempPath := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0600); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace auth file: %w", err)
	}
	ok = true
	return nil
}
