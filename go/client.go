package santati

import (
	"net/http"
	"strings"
	"time"

	"github.com/jamescarr/santati-sdks/go/internal/core"
)

const (
	defaultBaseURL        = "https://api.santati.io"
	defaultTimeout        = 10 * time.Second
	defaultMaxRetries     = 2
	defaultInitialBackoff = 250 * time.Millisecond
	defaultMaxBackoff     = 8 * time.Second
)

// Client is a Santati API client. Create one with NewClient and reuse it; it
// is safe for concurrent use.
type Client struct {
	// Events emits, lists and streams audit events.
	Events *Events

	trail          string
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration

	api *core.APIClient
}

type clientConfig struct {
	baseURL        string
	trail          string
	timeout        time.Duration
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	headers        map[string]string
}

// Option configures a Client.
type Option func(*clientConfig)

// WithBaseURL sets the API origin. A trailing slash is removed; the value may
// include a path prefix, so requests go to <baseURL>/api/v0/events/.
func WithBaseURL(baseURL string) Option {
	return func(c *clientConfig) { c.baseURL = baseURL }
}

// WithTrail sets the default trail emitted events are indexed on. It never
// applies to reads.
func WithTrail(trail string) Option {
	return func(c *clientConfig) { c.trail = trail }
}

// WithTimeout sets the per-attempt timeout. The default is 10s.
func WithTimeout(timeout time.Duration) Option {
	return func(c *clientConfig) { c.timeout = timeout }
}

// WithMaxRetries sets how many times a retryable failure is retried after the
// first attempt. The default is 2.
func WithMaxRetries(maxRetries int) Option {
	return func(c *clientConfig) { c.maxRetries = maxRetries }
}

// WithBackoff sets the retry backoff bounds. The default is 250ms and 8s.
func WithBackoff(initial, max time.Duration) Option {
	return func(c *clientConfig) {
		c.initialBackoff = initial
		c.maxBackoff = max
	}
}

// WithHeader adds a header sent with every request. The Authorization header
// is managed by the SDK and cannot be overridden.
func WithHeader(key, value string) Option {
	return func(c *clientConfig) { c.headers[key] = value }
}

// NewClient builds a client for a team API key. It returns a *Error with kind
// KindValidation for an empty key or an Authorization header in headers.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, validationError("api_key", "api_key must not be empty")
	}

	cfg := &clientConfig{
		baseURL:        defaultBaseURL,
		timeout:        defaultTimeout,
		maxRetries:     defaultMaxRetries,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
		headers:        map[string]string{},
	}
	for _, opt := range opts {
		opt(cfg)
	}
	for key := range cfg.headers {
		if strings.EqualFold(key, "authorization") {
			return nil, validationError("headers", "the Authorization header is managed by the SDK")
		}
	}

	baseURL := strings.TrimSuffix(cfg.baseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	coreCfg := core.NewConfiguration()
	coreCfg.Servers = core.ServerConfigurations{{URL: baseURL}}
	coreCfg.UserAgent = "santati-go/" + Version
	coreCfg.HTTPClient = &http.Client{
		Timeout: cfg.timeout,
		// A redirect is a response, never followed, so the key cannot be
		// replayed to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	coreCfg.AddDefaultHeader("Authorization", "Api-Key "+apiKey)
	for key, value := range cfg.headers {
		coreCfg.AddDefaultHeader(key, value)
	}

	c := &Client{
		trail:          cfg.trail,
		maxRetries:     cfg.maxRetries,
		initialBackoff: cfg.initialBackoff,
		maxBackoff:     cfg.maxBackoff,
		api:            core.NewAPIClient(coreCfg),
	}
	c.Events = &Events{client: c}
	return c, nil
}
