package santati

import (
	"context"

	"github.com/jamescarr/santati-sdks/go/internal/core"
)

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
	Type     string            `json:"type"`
	ID       string            `json:"id,omitempty"`
	Name     string            `json:"name,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// TargetInput describes one object an event acted on. Type and ID are required;
// an empty Name or nil Metadata is omitted from the request.
type TargetInput struct {
	Type     string            `json:"type"`
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// EventInput is one event to emit. Zero-valued fields are omitted from the
// request; Trail falls back to the client's default trail. The JSON encoding of
// an EventInput is the wire envelope.
type EventInput struct {
	Event          string            `json:"event"`
	Trail          string            `json:"trail,omitempty"`
	OrganizationID string            `json:"organization_id,omitempty"`
	Actor          *ActorInput       `json:"actor,omitempty"`
	Targets        []TargetInput     `json:"targets,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Data           any               `json:"data,omitempty"`
	Context        map[string]any    `json:"context,omitempty"`
	CreatedAt      string            `json:"created_at,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	// SchemaVersion pins the event to one published schema version of its
	// action; 0 means no pin. It is forwarded unchanged (as an int32) and never
	// validated here: the server answers an unusable pin with
	// KindSchemaValidation.
	SchemaVersion int `json:"schema_version,omitempty"`
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
// server replayed an earlier event for the same idempotency key. Event is nil
// and Queued true when the client has an outbox: the event was stored for the
// background worker instead of sent.
type EmitResult struct {
	Event          *AuditEvent
	Duplicate      bool
	IdempotencyKey string
	Queued         bool
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

	status int // HTTP status of the batch response (202 or 207)
}

// EventPage is one page of audit events. NextCursor is the decoded cursor of
// the next page, or nil on the last page.
type EventPage struct {
	Results    []AuditEvent
	NextCursor *string
}

// EventDefinition is one action of the team's catalog, re-exported from the
// generated core.
type EventDefinition = core.EventDefinition

// EventSchemaVersion is one version of an action's JSON Schema, re-exported
// from the generated core. Status is "draft", "published" or "deprecated".
type EventSchemaVersion = core.EventSchemaVersion

// SchemaCheck is the result of a dry run: how many stored events were run,
// how many the document would reject, and the first failures.
type SchemaCheck = core.SchemaCheck

// SchemaCheckFailure is one stored event a dry-run document would reject.
type SchemaCheckFailure = core.SchemaCheckFailure

// StandardEventCatalog lists every standard pack.
type StandardEventCatalog = core.StandardEventCatalog

// StandardPack is one standard pack and its actions.
type StandardPack = core.StandardPack

// StandardEvent is one action of a standard pack.
type StandardEvent = core.StandardEvent

// OcsfMapping is the OCSF class and activity a standard action maps to.
type OcsfMapping = core.OcsfMapping

// StandardPackInstallResult lists the actions an install defined and the ones
// the team already had.
type StandardPackInstallResult = core.StandardPackInstallResult

// PageParams selects a page of definitions or schema versions. Zero-valued
// fields are omitted. IterateDefinitions and IterateVersions ignore Cursor:
// iteration starts at the first page.
type PageParams struct {
	Limit  int
	Cursor string
}

// DefinitionInput is the definition CreateDefinition sends. Action is
// required; nil Description and IsActive and a nil AllowedTargetTypes are
// omitted, and a non-nil empty AllowedTargetTypes is sent as [].
type DefinitionInput struct {
	Action             string
	Description        *string
	AllowedTargetTypes []string
	IsActive           *bool
}

// DefinitionUpdate holds the changes UpdateDefinition sends: only the non-nil
// members, so the zero value sends {}. NewAction renames the action (it is
// sent as "action"); a non-nil empty AllowedTargetTypes is sent as [].
type DefinitionUpdate struct {
	NewAction          *string
	Description        *string
	AllowedTargetTypes []string
	IsActive           *bool
}

// DefinitionPage is one page of event definitions. NextCursor is the decoded
// cursor of the next page, or nil on the last page.
type DefinitionPage struct {
	Results    []EventDefinition
	NextCursor *string
}

// SchemaVersionPage is one page of an action's schema versions. NextCursor is
// the decoded cursor of the next page, or nil on the last page.
type SchemaVersionPage struct {
	Results    []EventSchemaVersion
	NextCursor *string
}

// SchemaVersionResult is a schema version and the ETag header the response
// carried ("" when there was none), to pass back as the ifMatch of
// UpdateVersion.
type SchemaVersionResult struct {
	SchemaVersion *EventSchemaVersion
	ETag          string
}

// OutboxEntry is one claimed outbox event. ID identifies it to Ack and
// Release.
type OutboxEntry struct {
	ID    string
	Event EventInput
}

// SendStatus is how one event's delivery ended.
type SendStatus string

// The delivery statuses a PostSendHook receives.
const (
	// SendAccepted means the server indexed the event.
	SendAccepted SendStatus = "accepted"
	// SendDuplicate means the server had already indexed the idempotency key.
	SendDuplicate SendStatus = "duplicate"
	// SendRejected means the server refused the event; Err holds why.
	SendRejected SendStatus = "rejected"
	// SendFailed means the event was not delivered; Err holds why.
	SendFailed SendStatus = "failed"
)

// SendOutcome is what a PostSendHook learns about one event. ID is set for
// accepted and duplicate events; Err is set for rejected and failed ones.
type SendOutcome struct {
	Status SendStatus
	ID     string
	Err    *Error
}

// PreSendHook is called once per event per worker pass, right before its batch
// request, with the stored event. Return the event (possibly modified) and
// true to send it, or false to drop it: a dropped event is removed from the
// outbox, never sent and never reported to the PostSendHook. A panic leaves
// the event in the outbox and reports a SendFailed outcome with an OutboxError
// of code "hook_failed".
type PreSendHook func(EventInput) (EventInput, bool)

// PostSendHook is called with the stored (original, not PreSendHook-modified)
// event and its outcome. A panic is recovered and ignored.
type PostSendHook func(EventInput, SendOutcome)

// OutboxStore is where a queued Emit keeps events until the worker sends them. A store
// must be safe for concurrent use. Failures should be returned as *Error; any
// other error is reported as OutboxError "store_unavailable".
type OutboxStore interface {
	// Enqueue stores an event at the tail. A full bounded store returns an
	// OutboxError with code "outbox_full".
	Enqueue(ctx context.Context, event EventInput) error
	// Claim returns up to limit of the oldest entries, FIFO. A claimed entry
	// is not returned again until it is released (or, for Redis, goes stale).
	Claim(ctx context.Context, limit int) ([]OutboxEntry, error)
	// Ack permanently deletes entries.
	Ack(ctx context.Context, ids []string) error
	// Release makes entries eligible for a later Claim.
	Release(ctx context.Context, ids []string) error
}
