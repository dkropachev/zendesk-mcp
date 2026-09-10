package main

import (
	"context"
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

	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

type loginInput struct {
	BaseURL string
	Cookie  string
	Headers map[string]string
}

var (
	urlPattern          = regexp.MustCompile(`https://[A-Za-z0-9.-]+\.zendesk\.com(?:/[^\s'"\\]*)?`)
	singleCookiePattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-b|--cookie)\s+'([^']+)'`)
	doubleCookiePattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-b|--cookie)\s+"([^"]+)"`)
	singleHeaderPattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-H|--header)\s+'([^']+)'`)
	doubleHeaderPattern = regexp.MustCompile(`(?m)(?:^|\s)(?:-H|--header)\s+"([^"]+)"`)
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

	client, err := zendesk.New(zendesk.Config{
		BaseURL:  parsed.BaseURL,
		AuthMode: "browser",
		Cookie:   parsed.Cookie,
	})
	if err != nil {
		return err
	}
	resp, err := client.Get(context.Background(), "/api/v2/users/me.json", nil, 256*1024)
	if err != nil {
		return fmt.Errorf("browser auth validation failed; no files changed: %w", err)
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
		return errors.New("browser auth validation returned no user")
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
	fmt.Fprintf(stdout, "Zendesk auth saved: user=%d role=%s name=%q\n", me.User.ID, me.User.Role, me.User.Name)
	fmt.Fprintf(stdout, "config:  %s\ncookie:  %s\nheaders: %s\n", paths.config, paths.cookie, paths.headers)
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
		switch name {
		case "User-Agent", "Accept-Language", "Referer":
			result.Headers[name] = value
		}
	}
	if result.Cookie == "" {
		return loginInput{}, errors.New("no browser cookies found; use Chrome DevTools Copy as cURL on an authenticated request")
	}
	return result, nil
}

type loginPaths struct {
	config  string
	cookie  string
	headers string
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
		config:  filepath.Join(dir, "config.json"),
		cookie:  filepath.Join(dir, "cookie"),
		headers: filepath.Join(dir, "headers.json"),
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
	config["auth_mode"] = "browser"
	config["cookie_file"] = paths.cookie
	config["headers_file"] = paths.headers
	config["enable_write"] = false
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
