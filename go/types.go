package santati

import "github.com/jamescarr/santati-sdks/go/internal/core"

// AuditEvent is an indexed audit event, re-exported from the generated core.
type AuditEvent = core.AuditEvent

// EventActor is the actor attached to an AuditEvent.
type EventActor = core.EventActor

// EventTarget is one of the objects an AuditEvent acted on.
type EventTarget = core.EventTarget

// ActorInput describes who produced an event. Type is required; ID is required
// unless Type is "anonymous"; an empty string means "not set" and is omitted
// from the request.
type ActorInput struct {
	Type     string
	ID       string
	Name     string
	Metadata map[string]string
}

// TargetInput describes one object an event acted on. Type and ID are required;
// an empty Name or nil Metadata is omitted from the request.
type TargetInput struct {
	Type     string
	ID       string
	Name     string
	Metadata map[string]string
}

// EventInput is one event to emit. Zero-valued fields are omitted from the
// request; Trail falls back to the client's default trail.
type EventInput struct {
	Event          string
	Trail          string
	OrganizationID string
	Actor          *ActorInput
	Targets        []TargetInput
	Metadata       map[string]string
	Data           any
	Context        map[string]any
	CreatedAt      string
	IdempotencyKey string
}

// ListParams filters a list request. Zero-valued fields are omitted, so the
// zero value lists everything.
type ListParams struct {
	Trail          string
	Event          string
	EventPrefix    string
	OrganizationID string
	ActorID        string
	ActorType      string
	TargetType     string
	TargetID       string
	CreatedAfter   string
	CreatedBefore  string
	Q              string
	Sort           string
	Cursor         string
	Limit          int
}

// EmitResult is the outcome of a single emit. Duplicate is true when the
// server replayed an earlier event for the same idempotency key.
type EmitResult struct {
	Event          *AuditEvent
	Duplicate      bool
	IdempotencyKey string
}

// BatchItemError explains why one event in a batch was rejected.
type BatchItemError struct {
	Code    string
	Message string
	Field   string
}

// BatchItem is the result for one event in a batch. ID is set for accepted and
// duplicate items; Error is set for rejected ones.
type BatchItem struct {
	Index  int
	Status string
	ID     string
	Error  *BatchItemError
}

// BatchResult is the outcome of a batch emit.
type BatchResult struct {
	Accepted int
	Rejected int
	Results  []BatchItem
}

// EventPage is one page of audit events. NextCursor is the decoded cursor of
// the next page, or nil on the last page.
type EventPage struct {
	Results    []AuditEvent
	NextCursor *string
}
