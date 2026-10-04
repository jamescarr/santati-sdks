package santati

import (
	"context"
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
	defaultBatchSize      = 100
	maxBatchSize          = 500
	defaultFlushInterval  = time.Second
)

// Client is a Santati API client. Create one with NewClient and reuse it; it
// is safe for concurrent use.
type Client struct {
	// Events emits, lists and streams audit events.
	Events *Events

	apiKey         string
	trail          string
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration

	api *core.APIClient

	outbox *outbox
}

type clientConfig struct {
	baseURL        string
	trail          string
	timeout        time.Duration
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	headers        map[string]string
	outbox         OutboxStore
	batchSize      int
	flushInterval  time.Duration
	preSend        PreSendHook
	postSend       PostSendHook
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

// WithOutbox makes Events.Emit store events in store for the background
// worker instead of sending them. Without it, Emit sends each event itself.
func WithOutbox(store OutboxStore) Option {
	return func(c *clientConfig) { c.outbox = store }
}

// WithBatchSize sets how many events the outbox worker sends per request. It
// must be between 1 and 500; the default is 100.
func WithBatchSize(batchSize int) Option {
	return func(c *clientConfig) { c.batchSize = batchSize }
}

// WithFlushInterval sets the outbox worker's tick. It must be positive; the
// default is 1s.
func WithFlushInterval(interval time.Duration) Option {
	return func(c *clientConfig) { c.flushInterval = interval }
}

// WithPreSend sets the hook called on each stored event right before its
// batch request. See PreSendHook.
func WithPreSend(hook PreSendHook) Option {
	return func(c *clientConfig) { c.preSend = hook }
}

// WithPostSend sets the hook called with the outcome of each sent event. See
// PostSendHook.
func WithPostSend(hook PostSendHook) Option {
	return func(c *clientConfig) { c.postSend = hook }
}

// NewClient builds a client for a team API key. It returns a *Error with kind
// KindValidation for an empty key, an Authorization header in headers, a batch
// size outside 1..500 or a non-positive flush interval.
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
		batchSize:      defaultBatchSize,
		flushInterval:  defaultFlushInterval,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.batchSize < 1 || cfg.batchSize > maxBatchSize {
		return nil, validationError("batch_size", "batch_size must be between 1 and 500")
	}
	if cfg.flushInterval <= 0 {
		return nil, validationError("flush_interval_ms", "flush_interval_ms must be greater than 0")
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
	for key, value := range cfg.headers {
		coreCfg.AddDefaultHeader(key, value)
	}

	c := &Client{
		apiKey:         apiKey,
		trail:          cfg.trail,
		maxRetries:     cfg.maxRetries,
		initialBackoff: cfg.initialBackoff,
		maxBackoff:     cfg.maxBackoff,
		api:            core.NewAPIClient(coreCfg),
	}
	c.Events = &Events{client: c}

	if cfg.outbox != nil {
		c.outbox = &outbox{
			client:    c,
			store:     cfg.outbox,
			batchSize: cfg.batchSize,
			interval:  cfg.flushInterval,
			preSend:   cfg.preSend,
			postSend:  cfg.postSend,
		}
	}
	return c, nil
}

// authorize returns ctx carrying the API key under the spec's ApiKeyAuth
// scheme, which is where the generated core reads it from.
func (c *Client) authorize(ctx context.Context) context.Context {
	return context.WithValue(ctx, core.ContextAPIKeys, map[string]core.APIKey{
		"ApiKeyAuth": {Key: c.apiKey, Prefix: "Api-Key"},
	})
}
