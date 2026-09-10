package zendesk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RateLimit struct {
	Limit      int           `json:"limit,omitempty"`
	Remaining  int           `json:"remaining,omitempty"`
	RetryAfter time.Duration `json:"-"`
}

type APIError struct {
	StatusCode int
	RequestID  string
	Message    string
	RetryAfter time.Duration
	RateLimit  RateLimit
}

func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	if e.StatusCode == http.StatusTooManyRequests && e.RetryAfter > 0 {
		return fmt.Sprintf("Zendesk API HTTP %d: %s; retry after %s", e.StatusCode, message, e.RetryAfter)
	}
	return fmt.Sprintf("Zendesk API HTTP %d: %s", e.StatusCode, message)
}

func newAPIError(status int, body []byte, header http.Header) *APIError {
	rate := parseRateLimit(header)
	return &APIError{
		StatusCode: status,
		RequestID:  firstHeader(header, "X-Zendesk-Request-Id", "X-Request-Id"),
		Message:    safeAPIMessage(body),
		RetryAfter: rate.RetryAfter,
		RateLimit:  rate,
	}
}

func parseRateLimit(header http.Header) RateLimit {
	result := RateLimit{
		Limit:     firstIntHeader(header, "X-Rate-Limit", "RateLimit-Limit"),
		Remaining: firstIntHeader(header, "X-Rate-Limit-Remaining", "RateLimit-Remaining"),
	}
	value := firstHeader(header, "Retry-After")
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		result.RetryAfter = time.Duration(seconds) * time.Second
	} else if when, err := http.ParseTime(value); err == nil {
		result.RetryAfter = time.Until(when)
		if result.RetryAfter < 0 {
			result.RetryAfter = 0
		}
	}
	return result
}

func firstIntHeader(header http.Header, names ...string) int {
	value := firstHeader(header, names...)
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func safeAPIMessage(body []byte) string {
	var value struct {
		Error       any    `json:"error"`
		Description string `json:"description"`
		Message     string `json:"message"`
	}
	if json.Unmarshal(body, &value) == nil {
		parts := make([]string, 0, 3)
		switch typed := value.Error.(type) {
		case string:
			parts = append(parts, typed)
		case map[string]any:
			if title, ok := typed["title"].(string); ok {
				parts = append(parts, title)
			}
		}
		parts = append(parts, value.Description, value.Message)
		for _, part := range parts {
			if cleaned := sanitizeText(part); cleaned != "" {
				return cleaned
			}
		}
	}
	return ""
}

func sanitizeText(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.Index(value, "http://"); index >= 0 {
		value = value[:index] + "[URL redacted]"
	}
	if index := strings.Index(value, "https://"); index >= 0 {
		value = value[:index] + "[URL redacted]"
	}
	if len(value) > 500 {
		value = value[:500] + "..."
	}
	return value
}
