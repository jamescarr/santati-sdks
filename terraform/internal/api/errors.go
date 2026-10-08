package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// APIError is a non-2xx response from the control plane. Redirects are never
// followed, so a 3xx is an APIError too.
type APIError struct {
	Status  int
	Code    string
	Message string
	// RetryAfter is the Retry-After header in whole seconds, when it has that form.
	RetryAfter *int
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is an APIError with status 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

// transportError is a request that produced no usable HTTP response.
type transportError struct{ err error }

func (e *transportError) Error() string { return "no HTTP response: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

var retryAfterDigits = regexp.MustCompile(`^\d+$`)

func parseRetryAfter(value string) *int {
	if !retryAfterDigits.MatchString(value) {
		return nil
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &seconds
}

// parseErrorBody reads the control plane's three error shapes, first match
// wins: the `{"error": {code, message, field}}` envelope, `{"detail": str}`,
// and DRF field errors `{"<field>": [str, …]}`.
func parseErrorBody(body []byte, status int) (code, message string) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if envelope, ok := payload["error"].(map[string]any); ok {
			if value, ok := envelope["code"].(string); ok {
				message, _ = envelope["message"].(string)
				if field, ok := envelope["field"].(string); ok && field != "" {
					message = field + ": " + message
				}
				return value, message
			}
		}
		if detail, ok := payload["detail"].(string); ok {
			return "", detail
		}
		if message, ok := fieldErrors(payload); ok {
			return "", message
		}
	}
	return "", "HTTP " + strconv.Itoa(status)
}

// fieldErrors renders `{"<field>": ["msg", …]}`, with nested objects flattened
// to `<field>.<subfield>`. It reports false when any value is neither a string
// array nor such an object, or when no messages result.
func fieldErrors(payload map[string]any) (string, bool) {
	messages := map[string]string{}
	if !collectFieldErrors("", payload, messages) || len(messages) == 0 {
		return "", false
	}
	keys := make([]string, 0, len(messages))
	for key := range messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = key + ": " + messages[key]
	}
	return strings.Join(parts, "; "), true
}

func collectFieldErrors(prefix string, object map[string]any, out map[string]string) bool {
	for key, value := range object {
		name := prefix + key
		switch v := value.(type) {
		case []any:
			texts := make([]string, 0, len(v))
			for _, item := range v {
				text, ok := item.(string)
				if !ok {
					return false
				}
				texts = append(texts, text)
			}
			if len(texts) > 0 {
				out[name] = strings.Join(texts, " ")
			}
		case map[string]any:
			if !collectFieldErrors(name+".", v, out) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
