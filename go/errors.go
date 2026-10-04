package santati

import "fmt"

// Kind is the class of a failure. Its string value is the kind's name.
type Kind string

// The eight error kinds.
const (
	// KindValidation covers local validation and HTTP 400, 413 and 422.
	KindValidation Kind = "ValidationError"
	// KindAuth covers HTTP 401 and 403.
	KindAuth Kind = "AuthError"
	// KindNotFound covers HTTP 404.
	KindNotFound Kind = "NotFoundError"
	// KindRateLimited covers HTTP 429.
	KindRateLimited Kind = "RateLimitedError"
	// KindServer covers HTTP 500-599.
	KindServer Kind = "ServerError"
	// KindTransport covers requests that never produced an HTTP response.
	KindTransport Kind = "TransportError"
	// KindAPI covers every other non-2xx, unexpected 2xx and undecodable 2xx.
	KindAPI Kind = "ApiError"
	// KindOutbox covers an outbox store that refused or failed (codes
	// "outbox_full", "store_unavailable", "closed") and a pre_send hook that
	// panicked (code "hook_failed"). Status is always 0.
	KindOutbox Kind = "OutboxError"
)

// Error is every failure the SDK reports. Status is 0 when there was no
// response, Code and Field are empty when the server sent none, and
// RetryAfter is nil when the server sent no Retry-After.
type Error struct {
	Kind       Kind
	Status     int
	Code       string
	Field      string
	RetryAfter *int
	Message    string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("santati: %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("santati: %s", e.Kind)
}

func validationError(field, message string) *Error {
	return &Error{Kind: KindValidation, Field: field, Message: message}
}

func transportError(err error) *Error {
	message := "no HTTP response"
	if err != nil {
		message = err.Error()
	}
	return &Error{Kind: KindTransport, Message: message}
}

func outboxError(code, message string) *Error {
	return &Error{Kind: KindOutbox, Code: code, Message: message}
}
