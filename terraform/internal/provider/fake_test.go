package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const fakeAPIKey = "test-key"

// recordedRequest is one request the fake control plane received.
type recordedRequest struct {
	Method string
	Path   string // path and query, as sent
	Body   map[string]any
}

type fakeStream struct {
	id        int64
	fields    map[string]any // what the client wrote: name, trail, status, destination_type, config, match_rules, auth
	createdAt string
	updatedAt string
}

type fakeTrail struct {
	name, region string
}

// fakeControlPlane is an in-process stand-in for the control plane's trails,
// streams, organizations and event definitions. It keeps what clients send
// exactly as sent (as the real server does) and records every request.
type fakeControlPlane struct {
	t      *testing.T
	server *httptest.Server

	mu           sync.Mutex
	requests     []recordedRequest
	trails       []fakeTrail
	streams      map[int64]*fakeStream
	nextStreamID int64
	clock        int
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	f := &fakeControlPlane{t: t, streams: map[int64]*fakeStream{}, nextStreamID: 1}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeControlPlane) URL() string { return f.server.URL }

// ---- inspection and out-of-band changes ----------------------------------

// all returns a snapshot of every request so far.
func (f *fakeControlPlane) all() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// mutations are the requests that are not GETs.
func (f *fakeControlPlane) mutations() []recordedRequest {
	var out []recordedRequest
	for _, r := range f.all() {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

// last returns the newest request with the method whose path starts with prefix.
func (f *fakeControlPlane) last(method, prefix string) (recordedRequest, error) {
	requests := f.all()
	for i := len(requests) - 1; i >= 0; i-- {
		if requests[i].Method == method && strings.HasPrefix(requests[i].Path, prefix) {
			return requests[i], nil
		}
	}
	return recordedRequest{}, fmt.Errorf("no %s %s request was recorded", method, prefix)
}

func (f *fakeControlPlane) count(method, prefix string) int {
	n := 0
	for _, r := range f.all() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// seedStream stores a stream as written by some other client, such as the
// dashboard, and returns its id.
func (f *fakeControlPlane) seedStream(write map[string]any) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.storeStream(write).id
}

func (f *fakeControlPlane) setStatus(id int64, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams[id].fields["status"] = status
}

func (f *fakeControlPlane) deleteStream(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.streams, id)
}

func (f *fakeControlPlane) addTrail(name, region string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trails = append(f.trails, fakeTrail{name, region})
}

func (f *fakeControlPlane) deleteTrail(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.trails[:0]
	for _, trail := range f.trails {
		if trail.name != name {
			kept = append(kept, trail)
		}
	}
	f.trails = kept
}

// ---- HTTP ----------------------------------------------------------------

func (f *fakeControlPlane) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, recordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: body})

	if r.Header.Get("Authorization") != "Api-Key "+fakeAPIKey || !strings.HasPrefix(r.UserAgent(), "santati-terraform/") {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "You do not have permission to perform this action."})
		return
	}

	path := r.URL.Path
	switch {
	case path == "/api/v0/trails/":
		f.trailCollection(w, r, body)
	case strings.HasPrefix(path, "/api/v0/trails/"):
		f.trailItem(w, r)
	case path == "/api/v0/streams/":
		f.streamCollection(w, r, body)
	case strings.HasPrefix(path, "/api/v0/streams/"):
		f.streamItem(w, r, body)
	case path == "/api/v0/organizations/":
		f.paged(w, r, organizationsFixture)
	case path == "/api/v0/event-definitions/":
		f.paged(w, r, eventDefinitionsFixture)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func trailJSON(t fakeTrail) map[string]any {
	return map[string]any{"name": t.name, "region": t.region, "ingest_url": "https://ingest.example/webhooks/" + t.name}
}

func (f *fakeControlPlane) trailCollection(w http.ResponseWriter, r *http.Request, body map[string]any) {
	switch r.Method {
	case http.MethodGet:
		out := make([]map[string]any, len(f.trails))
		for i, trail := range f.trails {
			out[i] = trailJSON(trail)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		name, _ := body["name"].(string)
		region, _ := body["region"].(string)
		if region == "" {
			region = "us-east"
		}
		for _, trail := range f.trails {
			if trail.name == name {
				writeJSON(w, http.StatusConflict, map[string]any{"detail": "source already exists (409): {'error': 'source_exists'}"})
				return
			}
		}
		trail := fakeTrail{name, region}
		f.trails = append(f.trails, trail)
		writeJSON(w, http.StatusCreated, trailJSON(trail))
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "Method not allowed."})
	}
}

func (f *fakeControlPlane) trailItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "Method not allowed."})
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v0/trails/"), "/")
	region := r.URL.Query().Get("region")
	for i, trail := range f.trails {
		if trail.name == name && (region == "" || trail.region == region) {
			f.trails = append(f.trails[:i], f.trails[i+1:]...)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
}

// ---- streams -------------------------------------------------------------

func (f *fakeControlPlane) tick() string {
	f.clock++
	return fmt.Sprintf("2026-01-01T00:00:%02dZ", f.clock%60)
}

// storeStream keeps the write as sent. Absent status and match_rules take the
// server's defaults; nothing else is normalised.
func (f *fakeControlPlane) storeStream(write map[string]any) *fakeStream {
	fields := map[string]any{}
	for key, value := range write {
		fields[key] = value
	}
	if _, ok := fields["status"]; !ok {
		fields["status"] = "active"
	}
	if _, ok := fields["match_rules"]; !ok {
		fields["match_rules"] = map[string]any{}
	}
	now := f.tick()
	stream := &fakeStream{id: f.nextStreamID, fields: fields, createdAt: now, updatedAt: now}
	f.streams[stream.id] = stream
	f.nextStreamID++
	return stream
}

func (f *fakeControlPlane) nameTaken(name string, except int64) bool {
	for id, stream := range f.streams {
		if id != except && stream.fields["name"] == name {
			return true
		}
	}
	return false
}

func streamJSON(s *fakeStream) map[string]any {
	// auth is write-only: no endpoint returns it.
	return map[string]any{
		"id":                   s.id,
		"name":                 s.fields["name"],
		"trail":                s.fields["trail"],
		"status":               s.fields["status"],
		"match_rules":          s.fields["match_rules"],
		"consecutive_failures": 0,
		"last_error":           "",
		"last_error_at":        nil,
		"resume_cursor":        nil,
		"destination": map[string]any{
			"name":             s.fields["name"],
			"destination_type": s.fields["destination_type"],
			"config":           s.fields["config"],
		},
		"created_at": s.createdAt,
		"updated_at": s.updatedAt,
	}
}

var duplicateStreamName = map[string]any{"name": []string{"A log stream with that name already exists."}}

func (f *fakeControlPlane) streamCollection(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "Method not allowed."})
		return
	}
	if name, _ := body["name"].(string); f.nameTaken(name, 0) {
		writeJSON(w, http.StatusBadRequest, duplicateStreamName)
		return
	}
	writeJSON(w, http.StatusCreated, streamJSON(f.storeStream(body)))
}

func (f *fakeControlPlane) streamItem(w http.ResponseWriter, r *http.Request, body map[string]any) {
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v0/streams/"), "/"), 10, 64)
	stream := f.streams[id]
	if err != nil || stream == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, streamJSON(stream))
	case http.MethodPatch:
		if name, ok := body["name"].(string); ok && f.nameTaken(name, id) {
			writeJSON(w, http.StatusBadRequest, duplicateStreamName)
			return
		}
		// Each top-level key sent replaces the stored one wholesale, so
		// config, auth and match_rules are never merged.
		for key, value := range body {
			stream.fields[key] = value
		}
		stream.updatedAt = f.tick()
		writeJSON(w, http.StatusOK, streamJSON(stream))
	case http.MethodDelete:
		delete(f.streams, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "Method not allowed."})
	}
}

// ---- paginated lists -----------------------------------------------------

const fakePageSize = 2

var organizationsFixture = []map[string]any{
	{"id": 1, "external_id": "org_acme", "name": "Acme", "retention_months": 12, "region": "us-east", "legal_hold": false, "created_at": "2026-01-01T00:00:00Z"},
	{"id": 2, "external_id": "org_globex", "name": "", "retention_months": 24, "region": "", "legal_hold": true, "created_at": "2026-01-02T00:00:00Z"},
	{"id": 3, "external_id": "org_initech", "name": "Initech", "retention_months": 6, "region": "eu-west", "legal_hold": false, "created_at": "2026-01-03T00:00:00Z"},
}

var eventDefinitionsFixture = []map[string]any{
	{"action": "invoice.created", "description": "An invoice was created.", "allowed_target_types": []string{"invoice"}, "is_active": true, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	{"action": "invoice.voided", "description": "An invoice was voided.", "allowed_target_types": []string{"invoice", "customer"}, "is_active": true, "created_at": "2026-01-02T00:00:00Z", "updated_at": "2026-01-03T00:00:00Z"},
	{"action": "user.login", "description": "A user signed in.", "allowed_target_types": []string{}, "is_active": false, "created_at": "2026-01-04T00:00:00Z", "updated_at": "2026-01-04T00:00:00Z"},
}

// paged serves items two to a page; the cursor is the index of the page's first
// item, and `next` is a full URL, as the real server's is.
func (f *fakeControlPlane) paged(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	start := 0
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(items) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Invalid cursor."})
			return
		}
		start = n
	}
	end := min(start+fakePageSize, len(items))

	var next any
	if end < len(items) {
		next = fmt.Sprintf("%s%s?cursor=%d", f.server.URL, r.URL.Path, end)
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": next, "previous": nil, "results": items[start:end]})
}
