package santati

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/jamescarr/santati-sdks/go/internal/core"
)

// Events emits, lists and streams audit events.
type Events struct {
	client *Client
}

// Emit indexes one audit event and returns the stored event. A repeated
// idempotency key within the server's replay window returns the earlier event
// with Duplicate set.
//
// It validates locally before sending: an empty Event fails with field "event"
// and an unresolved trail fails with field "trail".
func (e *Events) Emit(ctx context.Context, input EventInput) (*EmitResult, error) {
	envelope, key, err := e.client.envelope(input, "event", "trail")
	if err != nil {
		return nil, err
	}
	request := e.client.api.AuditEventsAPI.EventsCreate(e.client.authorize(ctx)).
		EventIngestRequest(core.EventEnvelopeRequestAsEventIngestRequest(envelope))
	return run(ctx, e.client, func() (*EmitResult, error) {
		return e.client.emitOnce(request, key)
	})
}

// EmitBatch indexes up to 500 events in one request and returns the per-item
// results. It validates every item before sending; the first failure is
// reported with its field ("events[<i>].event" or "events[<i>].trail").
func (e *Events) EmitBatch(ctx context.Context, events []EventInput) (*BatchResult, error) {
	if len(events) == 0 {
		return nil, validationError("events", "events must not be empty")
	}
	envelopes := make([]core.EventEnvelopeRequest, len(events))
	for i, input := range events {
		envelope, _, err := e.client.envelope(input,
			fmt.Sprintf("events[%d].event", i), fmt.Sprintf("events[%d].trail", i))
		if err != nil {
			return nil, err
		}
		envelopes[i] = *envelope
	}
	request := e.client.api.AuditEventsAPI.EventsCreate(e.client.authorize(ctx)).
		EventIngestRequest(core.EventBatchRequestAsEventIngestRequest(&core.EventBatchRequest{Events: envelopes}))
	return run(ctx, e.client, func() (*BatchResult, error) {
		return e.client.batchOnce(request)
	})
}

// List returns one page of audit events. Only the supplied filters are sent;
// the client's default trail never applies to reads.
func (e *Events) List(ctx context.Context, params ListParams) (*EventPage, error) {
	request := e.client.listRequest(ctx, params)
	return run(ctx, e.client, func() (*EventPage, error) {
		return e.client.listOnce(request)
	})
}

// Iterate lazily walks every page of audit events matching params, following
// NextCursor until the API reports no next page. An error on a later page is
// yielded after the events of the earlier pages. The cursor in params is
// ignored: iteration starts at the first page.
func (e *Events) Iterate(ctx context.Context, params ListParams) iter.Seq2[*AuditEvent, error] {
	return func(yield func(*AuditEvent, error) bool) {
		cursor := ""
		for {
			pageParams := params
			pageParams.Cursor = cursor
			page, err := e.List(ctx, pageParams)
			if err != nil {
				yield(nil, err)
				return
			}
			for i := range page.Results {
				if !yield(&page.Results[i], nil) {
					return
				}
			}
			if page.NextCursor == nil {
				return
			}
			cursor = *page.NextCursor
		}
	}
}

// envelope builds the request model for one event, resolving the trail and the
// idempotency key and omitting every unset member. eventField and trailField
// name the offending field for local validation failures.
func (c *Client) envelope(input EventInput, eventField, trailField string) (*core.EventEnvelopeRequest, string, error) {
	if input.Event == "" {
		return nil, "", validationError(eventField, "event is required")
	}
	trail := input.Trail
	if trail == "" {
		trail = c.trail
	}
	if trail == "" {
		return nil, "", validationError(trailField, "trail is required")
	}

	key := input.IdempotencyKey
	if key == "" {
		key = newUUID()
	}

	envelope := &core.EventEnvelopeRequest{Event: input.Event, Trail: trail, IdempotencyKey: &key}
	if input.CreatedAt != "" {
		envelope.CreatedAt = &input.CreatedAt
	}
	if input.OrganizationID != "" {
		envelope.OrganizationId = &input.OrganizationID
	}
	if input.Actor != nil {
		actor := &core.EventActorRequest{Type: input.Actor.Type}
		if input.Actor.ID != "" {
			actor.Id = &input.Actor.ID
		}
		if input.Actor.Name != "" {
			actor.Name = &input.Actor.Name
		}
		if input.Actor.Metadata != nil {
			actor.Metadata = &input.Actor.Metadata
		}
		envelope.Actor = actor
	}
	if input.Targets != nil {
		targets := make([]core.EventTargetRequest, len(input.Targets))
		for i, target := range input.Targets {
			request := core.EventTargetRequest{Type: target.Type, Id: target.ID}
			if target.Name != "" {
				request.Name = &target.Name
			}
			if target.Metadata != nil {
				request.Metadata = &target.Metadata
			}
			targets[i] = request
		}
		envelope.Targets = targets
	}
	if input.Metadata != nil {
		envelope.Metadata = &input.Metadata
	}
	if input.Data != nil {
		envelope.Data = input.Data
	}
	if input.Context != nil {
		envelope.Context = input.Context
	}
	return envelope, key, nil
}

func (c *Client) emitOnce(request core.ApiEventsCreateRequest, key string) (*EmitResult, error) {
	event, resp, err := request.Execute()
	if resp == nil {
		return nil, transportError(err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if err != nil {
			return nil, apiError(resp.StatusCode, "")
		}
		return &EmitResult{
			Event:          event,
			Duplicate:      resp.StatusCode == http.StatusOK,
			IdempotencyKey: key,
		}, nil
	default:
		if resp.StatusCode >= 300 {
			return nil, c.httpError(resp)
		}
		return nil, apiError(resp.StatusCode, "")
	}
}

func (c *Client) batchOnce(request core.ApiEventsCreateRequest) (*BatchResult, error) {
	_, resp, err := request.Execute()
	if resp == nil {
		return nil, transportError(err)
	}
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusMultiStatus:
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, apiError(resp.StatusCode, readErr.Error())
		}
		var result core.EventBatchResult
		if jsonErr := json.Unmarshal(body, &result); jsonErr != nil {
			return nil, apiError(resp.StatusCode, jsonErr.Error())
		}
		return toBatchResult(&result), nil
	default:
		if resp.StatusCode >= 300 {
			return nil, c.httpError(resp)
		}
		return nil, apiError(resp.StatusCode, "")
	}
}

func (c *Client) listOnce(request core.ApiEventsListRequest) (*EventPage, error) {
	page, resp, err := request.Execute()
	if resp == nil {
		return nil, transportError(err)
	}
	if resp.StatusCode >= 300 {
		return nil, c.httpError(resp)
	}
	if resp.StatusCode != http.StatusOK || err != nil {
		return nil, apiError(resp.StatusCode, "")
	}
	return toEventPage(page), nil
}

func (c *Client) listRequest(ctx context.Context, params ListParams) core.ApiEventsListRequest {
	request := c.api.AuditEventsAPI.EventsList(c.authorize(ctx))
	if params.ActorID != "" {
		request = request.ActorId(params.ActorID)
	}
	if params.ActorType != "" {
		request = request.ActorType(params.ActorType)
	}
	if params.CreatedAfter != "" {
		request = request.CreatedAfter(params.CreatedAfter)
	}
	if params.CreatedBefore != "" {
		request = request.CreatedBefore(params.CreatedBefore)
	}
	if params.Cursor != "" {
		request = request.Cursor(params.Cursor)
	}
	if params.Event != "" {
		request = request.Event(params.Event)
	}
	if params.EventPrefix != "" {
		request = request.EventPrefix(params.EventPrefix)
	}
	if params.Limit != 0 {
		request = request.Limit(int32(params.Limit))
	}
	if params.OrganizationID != "" {
		request = request.OrganizationId(params.OrganizationID)
	}
	if params.Q != "" {
		request = request.Q(params.Q)
	}
	if params.Sort != "" {
		request = request.Sort(params.Sort)
	}
	if params.TargetID != "" {
		request = request.TargetId(params.TargetID)
	}
	if params.TargetType != "" {
		request = request.TargetType(params.TargetType)
	}
	if params.Trail != "" {
		request = request.Trail(params.Trail)
	}
	return request
}

func toBatchResult(result *core.EventBatchResult) *BatchResult {
	out := &BatchResult{
		Accepted: int(result.Accepted),
		Rejected: int(result.Rejected),
		Results:  make([]BatchItem, len(result.Results)),
	}
	for i, item := range result.Results {
		batchItem := BatchItem{Index: int(item.Index), Status: item.Status}
		if item.Id != nil {
			batchItem.ID = *item.Id
		}
		if item.Error != nil {
			field := ""
			if item.Error.Field.Get() != nil {
				field = *item.Error.Field.Get()
			}
			batchItem.Error = &BatchItemError{
				Code:    item.Error.Code,
				Message: item.Error.Message,
				Field:   field,
			}
		}
		out.Results[i] = batchItem
	}
	return out
}

func toEventPage(page *core.PaginatedAuditEventList) *EventPage {
	out := &EventPage{Results: page.Results}
	if next := page.Next.Get(); next != nil {
		if parsed, err := url.Parse(*next); err == nil {
			if cursor := parsed.Query().Get("cursor"); cursor != "" {
				out.NextCursor = &cursor
			}
		}
	}
	return out
}

func (c *Client) httpError(resp *http.Response) *Error {
	var body []byte
	if resp.Body != nil {
		body, _ = io.ReadAll(resp.Body)
	}
	code, field, message := parseErrorBody(body, resp.StatusCode)
	return &Error{
		Kind:       kindForStatus(resp.StatusCode),
		Status:     resp.StatusCode,
		Code:       code,
		Field:      field,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		Message:    message,
	}
}

func kindForStatus(status int) Kind {
	switch {
	case status == 400 || status == 413 || status == 422:
		return KindValidation
	case status == 401 || status == 403:
		return KindAuth
	case status == 404:
		return KindNotFound
	case status == 429:
		return KindRateLimited
	case status >= 500 && status <= 599:
		return KindServer
	default:
		return KindAPI
	}
}

func parseErrorBody(body []byte, status int) (code, field, message string) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if envelope, ok := payload["error"].(map[string]any); ok {
			if value, ok := envelope["code"].(string); ok {
				code = value
				if value, ok := envelope["field"].(string); ok {
					field = value
				}
				if value, ok := envelope["message"].(string); ok {
					message = value
				}
				return code, field, message
			}
		}
		if detail, ok := payload["detail"].(string); ok {
			return "", "", detail
		}
	}
	return "", "", "HTTP " + strconv.Itoa(status)
}

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

func apiError(status int, message string) *Error {
	if message == "" {
		message = "HTTP " + strconv.Itoa(status)
	}
	return &Error{Kind: KindAPI, Status: status, Message: message}
}

// newUUID returns a lowercase random UUIDv4.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("santati: cannot read random bytes: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
