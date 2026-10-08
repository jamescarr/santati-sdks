package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// DefaultTimeoutSeconds is the delivery timeout the control plane applies when
// a stored config has none. The spec says 10; the server's effective default
// is 15, and what the server does is what a stream without a stored timeout
// actually gets.
const DefaultTimeoutSeconds = 15

// HTTPSConfig is an `https` destination's non-secret settings.
type HTTPSConfig struct {
	URL            string            `json:"url"`
	ContentType    string            `json:"content_type"`
	Headers        map[string]string `json:"headers,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds"`
}

// UnmarshalJSON reads a stored config the way the server's resolve_config does.
// The server keeps exactly what the client sent, so a stream created from the
// dashboard holds strings where the API form holds numbers and objects:
// `headers` as `Name: value` lines, `timeout_seconds` as "15" or "".
func (c *HTTPSConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	out := HTTPSConfig{ContentType: "json", TimeoutSeconds: DefaultTimeoutSeconds}
	out.URL, _ = raw["url"].(string)

	if contentType, _ := raw["content_type"].(string); strings.TrimSpace(contentType) != "" {
		out.ContentType = strings.ToLower(strings.TrimSpace(contentType))
	}

	headers, err := decodeHeaders(raw["headers"])
	if err != nil {
		return err
	}
	out.Headers = headers

	timeout, err := decodeTimeout(raw["timeout_seconds"])
	if err != nil {
		return err
	}
	out.TimeoutSeconds = timeout

	*c = out
	return nil
}

func decodeHeaders(value any) (map[string]string, error) {
	headers := map[string]string{}
	switch v := value.(type) {
	case nil:
	case map[string]any:
		for name, item := range v {
			switch s := item.(type) {
			case nil:
				headers[name] = ""
			case string:
				headers[name] = s
			default:
				headers[name] = fmt.Sprint(s)
			}
		}
	case string:
		for _, line := range strings.Split(v, "\n") {
			name, val, found := strings.Cut(line, ":")
			if name = strings.TrimSpace(name); !found || name == "" {
				continue
			}
			headers[name] = strings.TrimSpace(val)
		}
	default:
		return nil, fmt.Errorf("config.headers: expected an object or a string, got %T", value)
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return headers, nil
}

func decodeTimeout(value any) (int64, error) {
	switch v := value.(type) {
	case nil:
		return DefaultTimeoutSeconds, nil
	case float64:
		if seconds := int64(v); seconds != 0 {
			return seconds, nil
		}
		return DefaultTimeoutSeconds, nil
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return DefaultTimeoutSeconds, nil
		}
		seconds, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("config.timeout_seconds: %q is not a whole number", v)
		}
		if seconds == 0 {
			return DefaultTimeoutSeconds, nil
		}
		return seconds, nil
	default:
		return 0, fmt.Errorf("config.timeout_seconds: expected a number or a string, got %T", value)
	}
}

// HTTPSAuth is the credential header of an `https` destination. The server
// never returns it. Sent as `{}` it clears the stored secret.
type HTTPSAuth struct {
	HeaderName  string `json:"header_name,omitempty"`
	HeaderValue string `json:"header_value,omitempty"`
}

// MatchClause is one metadata clause of MatchRules.
type MatchClause struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// MatchRules selects which of a trail's events a stream forwards; the zero
// value forwards every event and encodes as `{}`.
type MatchRules struct {
	Actions         []string      `json:"actions,omitempty"`
	ActorTypes      []string      `json:"actor_types,omitempty"`
	OrganizationIDs []string      `json:"organization_ids,omitempty"`
	Metadata        []MatchClause `json:"metadata,omitempty"`
}

// StreamWrite is the body of a stream create or update. It always carries the
// whole of what the caller manages: the server replaces `config` and
// `match_rules` wholesale when sent.
type StreamWrite struct {
	Name            string      `json:"name"`
	Trail           string      `json:"trail"`
	DestinationType string      `json:"destination_type"`
	Config          HTTPSConfig `json:"config"`
	// Auth nil omits the key and keeps the stored secret; &HTTPSAuth{} sends
	// `{}` and clears it.
	Auth       *HTTPSAuth `json:"auth,omitempty"`
	MatchRules MatchRules `json:"match_rules"`
	Status     string     `json:"status"`
}

// Destination is the read shape of a stream's destination; it never carries auth.
type Destination struct {
	Name            string      `json:"name"`
	DestinationType string      `json:"destination_type"`
	Config          HTTPSConfig `json:"config"`
}

// Stream is the read shape of a log stream.
type Stream struct {
	ID                  int64       `json:"id"`
	Name                string      `json:"name"`
	Trail               string      `json:"trail"`
	Status              string      `json:"status"`
	MatchRules          MatchRules  `json:"match_rules"`
	ConsecutiveFailures int64       `json:"consecutive_failures"`
	LastError           string      `json:"last_error"`
	LastErrorAt         *string     `json:"last_error_at"`
	ResumeCursor        *string     `json:"resume_cursor"`
	Destination         Destination `json:"destination"`
	CreatedAt           string      `json:"created_at"`
	UpdatedAt           string      `json:"updated_at"`
}

const streamsPath = "/api/v0/streams/"

func streamPath(id int64) string { return streamsPath + strconv.FormatInt(id, 10) + "/" }

// CreateStream creates a log stream.
func (c *Client) CreateStream(ctx context.Context, in StreamWrite) (*Stream, error) {
	var stream Stream
	if err := c.do(ctx, http.MethodPost, streamsPath, nil, in, &stream); err != nil {
		return nil, err
	}
	return &stream, nil
}

// GetStream returns one log stream.
func (c *Client) GetStream(ctx context.Context, id int64) (*Stream, error) {
	var stream Stream
	if err := c.do(ctx, http.MethodGet, streamPath(id), nil, nil, &stream); err != nil {
		return nil, err
	}
	return &stream, nil
}

// UpdateStream PATCHes a log stream with the full body.
func (c *Client) UpdateStream(ctx context.Context, id int64, in StreamWrite) (*Stream, error) {
	var stream Stream
	if err := c.do(ctx, http.MethodPatch, streamPath(id), nil, in, &stream); err != nil {
		return nil, err
	}
	return &stream, nil
}

// DeleteStream deletes a log stream.
func (c *Client) DeleteStream(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, streamPath(id), nil, nil, nil)
}
