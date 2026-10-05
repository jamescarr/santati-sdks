package santati_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	santati "github.com/jamescarr/santati-sdks/go"
)

const casesGlob = "../conformance/cases/*.json"

// TestConformance runs the language-neutral vectors in ../conformance/cases
// against the public API, one subtest per case id.
func TestConformance(t *testing.T) {
	files, err := filepath.Glob(casesGlob)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no conformance cases found at %s", casesGlob)
	}

	total := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Cases []json.RawMessage `json:"cases"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, caseRaw := range document.Cases {
			var testCase testCase
			if err := json.Unmarshal(caseRaw, &testCase); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			t.Run(testCase.ID, func(t *testing.T) { runCase(t, testCase) })
			total++
		}
	}
	if total == 0 {
		t.Fatal("no cases loaded")
	}
}

type testCase struct {
	ID        string          `json:"id"`
	Feature   string          `json:"feature"`
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input"`
	Expect    json.RawMessage `json:"expect"`
}

type caseInput struct {
	Client  clientJSON                 `json:"client"`
	Gateway map[string]json.RawMessage `json:"gateway"`
	Event   json.RawMessage            `json:"event"`
	Events  []json.RawMessage          `json:"events"`
	Params  json.RawMessage            `json:"params"`
	Hooks   *hooksJSON                 `json:"hooks"`
}

type hooksJSON struct {
	PreSend *struct {
		SetMetadata map[string]string `json:"set_metadata"`
		DropEvents  []string          `json:"drop_events"`
		Raise       bool              `json:"raise"`
	} `json:"pre_send"`
	PostSend *struct {
		Raise bool `json:"raise"`
	} `json:"post_send"`
}

type clientJSON struct {
	APIKey           string            `json:"api_key"`
	Trail            string            `json:"trail"`
	TimeoutMS        *float64          `json:"timeout_ms"`
	MaxRetries       *int              `json:"max_retries"`
	InitialBackoffMS *float64          `json:"initial_backoff_ms"`
	MaxBackoffMS     *float64          `json:"max_backoff_ms"`
	Headers          map[string]string `json:"headers"`
	BasePath         string            `json:"base_path"`
	BatchSize        *int              `json:"batch_size"`
	FlushIntervalMS  *float64          `json:"flush_interval_ms"`
	MaxPending       *int              `json:"max_pending"`
}

func runCase(t *testing.T, testCase testCase) {
	t.Helper()

	var input caseInput
	if err := json.Unmarshal(testCase.Input, &input); err != nil {
		t.Fatalf("input: %v", err)
	}
	expect := parseExpect(testCase.Expect)

	origin := "http://127.0.0.1:1"
	var gateway *mockGateway
	if !isUnreachable(input.Gateway) {
		gateway = &mockGateway{responses: gatewayResponses(input.Gateway)}
		server := httptest.NewServer(gateway)
		defer server.Close()
		origin = server.URL
	}

	outcomes := &outcomeLog{}
	client, clientErr := buildClient(input.Client, origin, testCase.Operation == "emit_outbox", hookOptions(input.Hooks, outcomes)...)
	bindings := map[string]string{}

	if clientErr != nil {
		finish(t, expect, nil, clientErr, gateway, bindings)
		return
	}

	ctx := context.Background()
	var actual any
	var err error
	switch testCase.Operation {
	case "emit":
		var result *santati.EmitResult
		result, err = client.Events.Emit(ctx, decodeEvent(input.Event))
		if err == nil {
			actual = emitValue(result)
		}
	case "emit_batch":
		var result *santati.BatchResult
		result, err = client.Events.EmitBatch(ctx, decodeEvents(input.Events))
		if err == nil {
			actual = batchValue(result)
		}
	case "list":
		var page *santati.EventPage
		page, err = client.Events.List(ctx, decodeParams(input.Params))
		if err == nil {
			actual = pageValue(page)
		}
	case "iterate":
		var events []*santati.AuditEvent
		events, err = collectIterate(ctx, client, decodeParams(input.Params))
		if err == nil {
			actual = events
		}
	case "emit_outbox":
		actual, err = runEmitOutbox(ctx, client, decodeEvents(input.Events), outcomes)
	default:
		t.Fatalf("unknown operation %q", testCase.Operation)
	}
	finish(t, expect, actual, err, gateway, bindings)
}

func finish(t *testing.T, expect expectation, actual any, err error, gateway *mockGateway, bindings map[string]string) {
	t.Helper()
	if expect.HasError {
		if err == nil {
			t.Fatalf("expected error %s, got %s", mustJSON(expect.Error), mustJSON(toJSONValue(actual)))
		}
		checkError(t, expect.Error, err)
	} else {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := stripNulls(expect.OK)
		got := stripNulls(toJSONValue(actual))
		if matchErr := match(want, got, bindings); matchErr != nil {
			t.Fatalf("result mismatch: %v\nexpected: %s\nactual:   %s", matchErr, mustJSON(want), mustJSON(got))
		}
	}
	checkRequests(t, expect, gateway, bindings)
}

// ---- expectations --------------------------------------------------------

type expectedRequest struct {
	Method  string
	Path    string
	Headers map[string]string
	HasBody bool
	Body    any
}

type expectation struct {
	HasOK       bool
	OK          any
	HasError    bool
	Error       map[string]any
	HasRequests bool
	Requests    []expectedRequest
}

func parseExpect(raw json.RawMessage) expectation {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		panic(err)
	}
	var expect expectation
	if value, ok := fields["ok"]; ok {
		expect.HasOK = true
		if err := json.Unmarshal(value, &expect.OK); err != nil {
			panic(err)
		}
	}
	if value, ok := fields["error"]; ok {
		expect.HasError = true
		if err := json.Unmarshal(value, &expect.Error); err != nil {
			panic(err)
		}
	}
	if value, ok := fields["requests"]; ok {
		expect.HasRequests = true
		var requests []map[string]json.RawMessage
		if err := json.Unmarshal(value, &requests); err != nil {
			panic(err)
		}
		for _, request := range requests {
			expected := expectedRequest{}
			_ = json.Unmarshal(request["method"], &expected.Method)
			_ = json.Unmarshal(request["path"], &expected.Path)
			if headers, ok := request["headers"]; ok {
				_ = json.Unmarshal(headers, &expected.Headers)
			}
			if body, ok := request["body"]; ok {
				expected.HasBody = true
				_ = json.Unmarshal(body, &expected.Body)
			}
			expect.Requests = append(expect.Requests, expected)
		}
	}
	return expect
}

func checkError(t *testing.T, want map[string]any, err error) {
	t.Helper()
	sdkErr, ok := err.(*santati.Error)
	if !ok {
		t.Fatalf("error %T is not *santati.Error: %v", err, err)
	}
	if value, ok := want["kind"]; ok {
		kind, _ := value.(string)
		if string(sdkErr.Kind) != kind {
			t.Fatalf("kind = %q, want %q", sdkErr.Kind, kind)
		}
	}
	if value, ok := want["status"]; ok {
		if want2 := intOrNull(value); want2 == nil {
			if sdkErr.Status != 0 {
				t.Fatalf("status = %d, want null", sdkErr.Status)
			}
		} else if sdkErr.Status != *want2 {
			t.Fatalf("status = %d, want %d", sdkErr.Status, *want2)
		}
	}
	if value, ok := want["retry_after"]; ok {
		if want2 := intOrNull(value); want2 == nil {
			if sdkErr.RetryAfter != nil {
				t.Fatalf("retry_after = %d, want null", *sdkErr.RetryAfter)
			}
		} else if sdkErr.RetryAfter == nil || *sdkErr.RetryAfter != *want2 {
			t.Fatalf("retry_after = %v, want %d", sdkErr.RetryAfter, *want2)
		}
	}
	if value, ok := want["code"]; ok {
		if got, want2 := sdkErr.Code, strOrEmpty(value); got != want2 {
			t.Fatalf("code = %q, want %q", got, want2)
		}
	}
	if value, ok := want["field"]; ok {
		if got, want2 := sdkErr.Field, strOrEmpty(value); got != want2 {
			t.Fatalf("field = %q, want %q", got, want2)
		}
	}
}

func checkRequests(t *testing.T, expect expectation, gateway *mockGateway, bindings map[string]string) {
	t.Helper()
	if !expect.HasRequests {
		return
	}
	var recorded []recordedRequest
	if gateway != nil {
		recorded = gateway.snapshot()
	}
	if len(recorded) != len(expect.Requests) {
		t.Fatalf("recorded %d requests, want %d", len(recorded), len(expect.Requests))
	}
	for i, want := range expect.Requests {
		have := recorded[i]
		if have.Method != want.Method {
			t.Fatalf("request %d method = %s, want %s", i, have.Method, want.Method)
		}
		if have.Path != want.Path {
			t.Fatalf("request %d path = %s, want %s", i, have.Path, want.Path)
		}
		for name, value := range want.Headers {
			if have.Headers[name] != value {
				t.Fatalf("request %d header %s = %q, want %q", i, name, have.Headers[name], value)
			}
		}
		if want.HasBody {
			if matchErr := match(want.Body, have.Body, bindings); matchErr != nil {
				t.Fatalf("request %d body: %v\nexpected: %s\nactual:   %s",
					i, matchErr, mustJSON(want.Body), mustJSON(have.Body))
			}
		}
	}
}

// ---- mock gateway --------------------------------------------------------

type recordedRequest struct {
	Method  string
	Path    string
	Headers map[string]string
	Body    any
}

type gatewayResponse struct {
	Status             int
	Headers            map[string]string
	Body               []byte
	DefaultContentType string
	DelayMS            int
}

type mockGateway struct {
	mu        sync.Mutex
	requests  []recordedRequest
	responses []gatewayResponse
}

func (m *mockGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	headers := map[string]string{}
	for name, values := range r.Header {
		if len(values) > 0 {
			headers[strings.ToLower(name)] = values[0]
		}
	}

	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, recordedRequest{Method: r.Method, Path: r.RequestURI, Headers: headers, Body: body})
	response := m.responses[min(index, len(m.responses)-1)]
	m.mu.Unlock()

	if response.DelayMS > 0 {
		time.Sleep(time.Duration(response.DelayMS) * time.Millisecond)
	}

	contentType := response.DefaultContentType
	for name, value := range response.Headers {
		if strings.EqualFold(name, "content-type") {
			contentType = value
		} else {
			w.Header().Set(name, value)
		}
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(response.Body)))
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}

func (m *mockGateway) snapshot() []recordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recordedRequest(nil), m.requests...)
}

func isUnreachable(gateway map[string]json.RawMessage) bool {
	value, ok := gateway["unreachable"]
	if !ok {
		return false
	}
	var unreachable bool
	_ = json.Unmarshal(value, &unreachable)
	return unreachable
}

func gatewayResponses(gateway map[string]json.RawMessage) []gatewayResponse {
	if sequence, ok := gateway["sequence"]; ok {
		var raw []map[string]json.RawMessage
		if err := json.Unmarshal(sequence, &raw); err != nil {
			panic(err)
		}
		responses := make([]gatewayResponse, len(raw))
		for i, each := range raw {
			responses[i] = responseFrom(each)
		}
		return responses
	}
	return []gatewayResponse{responseFrom(gateway)}
}

func responseFrom(raw map[string]json.RawMessage) gatewayResponse {
	var response gatewayResponse
	if value, ok := raw["status"]; ok {
		_ = json.Unmarshal(value, &response.Status)
	}
	if value, ok := raw["headers"]; ok {
		_ = json.Unmarshal(value, &response.Headers)
	}
	if value, ok := raw["delay_ms"]; ok {
		_ = json.Unmarshal(value, &response.DelayMS)
	}
	if value, ok := raw["body"]; ok {
		var body map[string]json.RawMessage
		_ = json.Unmarshal(value, &body)
		if jsonBody, ok := body["json"]; ok {
			var compact bytes.Buffer
			_ = json.Compact(&compact, jsonBody)
			response.Body = compact.Bytes()
			response.DefaultContentType = "application/json"
		} else if textBody, ok := body["text"]; ok {
			var text string
			_ = json.Unmarshal(textBody, &text)
			response.Body = []byte(text)
			response.DefaultContentType = "text/plain; charset=utf-8"
		}
	}
	return response
}

// ---- inputs --------------------------------------------------------------

func buildClient(c clientJSON, origin string, outbox bool, extra ...santati.Option) (*santati.Client, error) {
	options := []santati.Option{santati.WithBaseURL(origin + c.BasePath)}
	options = append(options, extra...)
	if c.BatchSize != nil {
		options = append(options, santati.WithBatchSize(*c.BatchSize))
	}
	if c.FlushIntervalMS != nil {
		options = append(options, santati.WithFlushInterval(time.Duration(*c.FlushIntervalMS*float64(time.Millisecond))))
	}
	if outbox {
		maxPending := 10000
		if c.MaxPending != nil {
			maxPending = *c.MaxPending
		}
		store, err := santati.NewMemoryOutbox(maxPending)
		if err != nil {
			return nil, err
		}
		options = append(options, santati.WithOutbox(store))
	}
	if c.Trail != "" {
		options = append(options, santati.WithTrail(c.Trail))
	}
	if c.TimeoutMS != nil {
		options = append(options, santati.WithTimeout(time.Duration(*c.TimeoutMS)*time.Millisecond))
	}
	if c.MaxRetries != nil {
		options = append(options, santati.WithMaxRetries(*c.MaxRetries))
	}
	if c.InitialBackoffMS != nil || c.MaxBackoffMS != nil {
		initial := 250 * time.Millisecond
		maxBackoff := 8 * time.Second
		if c.InitialBackoffMS != nil {
			initial = time.Duration(*c.InitialBackoffMS) * time.Millisecond
		}
		if c.MaxBackoffMS != nil {
			maxBackoff = time.Duration(*c.MaxBackoffMS) * time.Millisecond
		}
		options = append(options, santati.WithBackoff(initial, maxBackoff))
	}
	names := make([]string, 0, len(c.Headers))
	for name := range c.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		options = append(options, santati.WithHeader(name, c.Headers[name]))
	}
	return santati.NewClient(c.APIKey, options...)
}

type wireActor struct {
	Type     string            `json:"type"`
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata"`
}

type wireTarget struct {
	Type     string            `json:"type"`
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata"`
}

type wireEvent struct {
	Event          string            `json:"event"`
	Trail          string            `json:"trail"`
	OrganizationID string            `json:"organization_id"`
	Actor          *wireActor        `json:"actor"`
	Targets        []wireTarget      `json:"targets"`
	Metadata       map[string]string `json:"metadata"`
	Data           any               `json:"data"`
	Context        map[string]any    `json:"context"`
	CreatedAt      string            `json:"created_at"`
	IdempotencyKey string            `json:"idempotency_key"`
}

func decodeEvent(raw json.RawMessage) santati.EventInput {
	var wire wireEvent
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &wire)
	}
	input := santati.EventInput{
		Event:          wire.Event,
		Trail:          wire.Trail,
		OrganizationID: wire.OrganizationID,
		Metadata:       wire.Metadata,
		Data:           wire.Data,
		Context:        wire.Context,
		CreatedAt:      wire.CreatedAt,
		IdempotencyKey: wire.IdempotencyKey,
	}
	if wire.Actor != nil {
		input.Actor = &santati.ActorInput{
			Type:     wire.Actor.Type,
			ID:       wire.Actor.ID,
			Name:     wire.Actor.Name,
			Metadata: wire.Actor.Metadata,
		}
	}
	if wire.Targets != nil {
		input.Targets = make([]santati.TargetInput, len(wire.Targets))
		for i, target := range wire.Targets {
			input.Targets[i] = santati.TargetInput{
				Type:     target.Type,
				ID:       target.ID,
				Name:     target.Name,
				Metadata: target.Metadata,
			}
		}
	}
	return input
}

func decodeEvents(raws []json.RawMessage) []santati.EventInput {
	events := make([]santati.EventInput, len(raws))
	for i, raw := range raws {
		events[i] = decodeEvent(raw)
	}
	return events
}

type wireParams struct {
	Trail          string `json:"trail"`
	Event          string `json:"event"`
	EventPrefix    string `json:"event_prefix"`
	OrganizationID string `json:"organization_id"`
	ActorID        string `json:"actor_id"`
	ActorType      string `json:"actor_type"`
	TargetType     string `json:"target_type"`
	TargetID       string `json:"target_id"`
	CreatedAfter   string `json:"created_after"`
	CreatedBefore  string `json:"created_before"`
	Q              string `json:"q"`
	Sort           string `json:"sort"`
	Limit          int    `json:"limit"`
	Cursor         string `json:"cursor"`
}

func decodeParams(raw json.RawMessage) santati.ListParams {
	var wire wireParams
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &wire)
	}
	return santati.ListParams{
		Trail:          wire.Trail,
		Event:          wire.Event,
		EventPrefix:    wire.EventPrefix,
		OrganizationID: wire.OrganizationID,
		ActorID:        wire.ActorID,
		ActorType:      wire.ActorType,
		TargetType:     wire.TargetType,
		TargetID:       wire.TargetID,
		CreatedAfter:   wire.CreatedAfter,
		CreatedBefore:  wire.CreatedBefore,
		Q:              wire.Q,
		Sort:           wire.Sort,
		Limit:          wire.Limit,
		Cursor:         wire.Cursor,
	}
}

func collectIterate(ctx context.Context, client *santati.Client, params santati.ListParams) ([]*santati.AuditEvent, error) {
	events := []*santati.AuditEvent{}
	for event, err := range client.Events.Iterate(ctx, params) {
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
	return events, nil
}

// ---- emit_outbox ---------------------------------------------------------

// outcomeLog collects what the runner's post_send hook sees. The hook runs on
// the worker goroutine, so access is locked.
type outcomeLog struct {
	mu      sync.Mutex
	entries []any
}

func (o *outcomeLog) add(entry any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.entries = append(o.entries, entry)
}

func (o *outcomeLog) snapshot() []any {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]any{}, o.entries...)
}

// hookOptions registers the case's pre_send hook (when present) and always a
// post_send hook that records the outcome, per conformance/README.md.
func hookOptions(hooks *hooksJSON, outcomes *outcomeLog) []santati.Option {
	var options []santati.Option
	if hooks != nil && hooks.PreSend != nil {
		spec := hooks.PreSend
		options = append(options, santati.WithPreSend(func(event santati.EventInput) (santati.EventInput, bool) {
			if spec.Raise {
				panic("conformance pre_send")
			}
			if slices.Contains(spec.DropEvents, event.Event) {
				return event, false
			}
			if spec.SetMetadata != nil {
				merged := map[string]string{}
				maps.Copy(merged, event.Metadata)
				maps.Copy(merged, spec.SetMetadata)
				event.Metadata = merged
			}
			return event, true
		}))
	}
	raise := hooks != nil && hooks.PostSend != nil && hooks.PostSend.Raise
	options = append(options, santati.WithPostSend(func(event santati.EventInput, outcome santati.SendOutcome) {
		entry := map[string]any{"event": toJSONValue(event), "status": string(outcome.Status)}
		if outcome.ID != "" {
			entry["id"] = outcome.ID
		}
		if outcome.Err != nil {
			var retryAfter any
			if outcome.Err.RetryAfter != nil {
				retryAfter = *outcome.Err.RetryAfter
			}
			var status any
			if outcome.Err.Status != 0 {
				status = outcome.Err.Status
			}
			entry["error"] = map[string]any{
				"kind":        string(outcome.Err.Kind),
				"status":      status,
				"code":        nullIfEmpty(outcome.Err.Code),
				"field":       nullIfEmpty(outcome.Err.Field),
				"retry_after": retryAfter,
			}
		}
		outcomes.add(entry)
		if raise {
			panic("conformance post_send")
		}
	}))
	return options
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func runEmitOutbox(ctx context.Context, client *santati.Client, events []santati.EventInput, outcomes *outcomeLog) (any, error) {
	results := []any{}
	var emitErr error
	for _, event := range events {
		result, err := client.Events.Emit(ctx, event)
		if err != nil {
			emitErr = err
			break
		}
		results = append(results, emitValue(result))
	}
	if err := client.Close(ctx); err != nil && emitErr == nil {
		emitErr = err
	}
	if emitErr != nil {
		return nil, emitErr
	}
	return map[string]any{"results": results, "outcomes": outcomes.snapshot()}, nil
}

func emitValue(result *santati.EmitResult) any {
	return map[string]any{
		"event":           result.Event,
		"duplicate":       result.Duplicate,
		"idempotency_key": result.IdempotencyKey,
		"queued":          result.Queued,
	}
}

// ---- result values -------------------------------------------------------

func batchValue(result *santati.BatchResult) any {
	results := make([]any, len(result.Results))
	for i, item := range result.Results {
		value := map[string]any{"index": item.Index, "status": item.Status}
		if item.ID != "" {
			value["id"] = item.ID
		}
		if item.Error != nil {
			value["error"] = map[string]any{
				"code":    item.Error.Code,
				"message": item.Error.Message,
				"field":   item.Error.Field,
			}
		}
		results[i] = value
	}
	return map[string]any{
		"accepted": result.Accepted,
		"rejected": result.Rejected,
		"results":  results,
	}
}

func pageValue(page *santati.EventPage) any {
	value := map[string]any{"results": page.Results}
	if page.NextCursor != nil {
		value["next_cursor"] = *page.NextCursor
	} else {
		value["next_cursor"] = nil
	}
	return value
}

// ---- matching ------------------------------------------------------------

func match(expected, actual any, bindings map[string]string) error {
	if expectMap, ok := expected.(map[string]any); ok {
		if len(expectMap) == 1 {
			if label, ok := expectMap["$generated"].(string); ok {
				return bindGenerated(label, actual, bindings)
			}
		}
		actualMap, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object, got %s", mustJSON(actual))
		}
		if len(expectMap) != len(actualMap) {
			return fmt.Errorf("expected %d members, got %d", len(expectMap), len(actualMap))
		}
		for key, expectValue := range expectMap {
			actualValue, ok := actualMap[key]
			if !ok {
				return fmt.Errorf("missing member %q", key)
			}
			if err := match(expectValue, actualValue, bindings); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		return nil
	}
	if expectSlice, ok := expected.([]any); ok {
		actualSlice, ok := actual.([]any)
		if !ok {
			return fmt.Errorf("expected array, got %s", mustJSON(actual))
		}
		if len(expectSlice) != len(actualSlice) {
			return fmt.Errorf("expected %d items, got %d", len(expectSlice), len(actualSlice))
		}
		for i := range expectSlice {
			if err := match(expectSlice[i], actualSlice[i], bindings); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
		return nil
	}
	if !reflect.DeepEqual(expected, actual) {
		return fmt.Errorf("expected %s, got %s", mustJSON(expected), mustJSON(actual))
	}
	return nil
}

func bindGenerated(label string, actual any, bindings map[string]string) error {
	value, ok := actual.(string)
	if !ok || value == "" {
		return fmt.Errorf("$generated %q: expected a non-empty string, got %s", label, mustJSON(actual))
	}
	if previous, ok := bindings[label]; ok {
		if previous != value {
			return fmt.Errorf("$generated %q bound to %q and %q", label, previous, value)
		}
		return nil
	}
	for other, otherValue := range bindings {
		if otherValue == value {
			return fmt.Errorf("$generated %q reuses the value of %q", label, other)
		}
	}
	bindings[label] = value
	return nil
}

func stripNulls(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if item == nil {
				continue
			}
			out[key] = stripNulls(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = stripNulls(item)
		}
		return out
	default:
		return value
	}
}

func toJSONValue(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return value
	}
	return decoded
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%#v", value)
	}
	return string(encoded)
}

func intOrNull(value any) *int {
	if value == nil {
		return nil
	}
	number, ok := value.(float64)
	if !ok {
		return nil
	}
	result := int(number)
	return &result
}

func strOrEmpty(value any) string {
	if value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
