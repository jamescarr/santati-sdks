package santati

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// run calls operation until it succeeds, the error is not retryable, the
// client's attempts are exhausted, or the context is cancelled. Every attempt
// sends the identical request, so generated idempotency keys and bodies are
// stable.
func run[T any](ctx context.Context, c *Client, operation func() (T, error)) (T, error) {
	return runAttempts(ctx, c, c.maxRetries, operation)
}

// runAttempts is run with an explicit retry budget: maxRetries 0 sends once.
func runAttempts[T any](ctx context.Context, c *Client, maxRetries int, operation func() (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		value, err := operation()
		if err == nil {
			return value, nil
		}
		var sdkErr *Error
		if !errors.As(err, &sdkErr) || attempt > maxRetries || !retryable(sdkErr) {
			return zero, err
		}
		delay, stop := c.retryDelay(attempt, sdkErr)
		if stop {
			return zero, err
		}
		if err := sleep(ctx, delay); err != nil {
			return zero, transportError(err)
		}
	}
}

// retryable reports whether the SDK may retry the failure.
func retryable(e *Error) bool {
	switch e.Kind {
	case KindTransport:
		return true
	case KindServer:
		return e.Status == 500 || e.Status == 502 || e.Status == 503 || e.Status == 504
	case KindRateLimited:
		return e.Code != "quota_exceeded"
	default:
		return false
	}
}

// retryDelay returns how long to wait before retry number attempt (1-based),
// and whether the caller must stop and raise the error instead.
func (c *Client) retryDelay(attempt int, e *Error) (time.Duration, bool) {
	if e.RetryAfter != nil {
		delay := time.Duration(*e.RetryAfter) * time.Second
		if delay > c.maxBackoff {
			return 0, true
		}
		return delay, false
	}
	ceiling := c.initialBackoff * time.Duration(int64(1)<<uint(attempt-1))
	if ceiling <= 0 || ceiling > c.maxBackoff {
		ceiling = c.maxBackoff
	}
	if ceiling <= 0 {
		return 0, false
	}
	return time.Duration(rand.Int64N(int64(ceiling) + 1)), false
}

// sleep waits for the delay, returning early when the context is done.
func sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
