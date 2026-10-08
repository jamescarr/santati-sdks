package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a Client at url with sub-millisecond backoffs so retry
// tests do not sleep.
func newTestClient(url string) *Client {
	c := New(Options{BaseURL: url, APIKey: "k", UserAgent: "santati-terraform/test"})
	c.initialBackoff = time.Millisecond
	c.maxBackoff = time.Millisecond
	return c
}

func TestGetRetriesServerErrors(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`[{"name":"billing","region":"us-east","ingest_url":"https://i/billing"}]`))
	}))
	defer srv.Close()

	trails, err := newTestClient(srv.URL).ListTrails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if len(trails) != 1 || trails[0].Name != "billing" || trails[0].IngestURL != "https://i/billing" {
		t.Fatalf("trails = %+v", trails)
	}
}

func TestPostIsNotRetriedOnServerError(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail":"Unknown region 'eu-west'."}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).CreateTrail(context.Background(), TrailCreate{Name: "billing", Region: "eu-west"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Fatalf("err = %v, want APIError 503", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1: a timed-out POST may have executed", requests.Load())
	}
}

func TestPostIsRetriedOn429(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"billing","region":"us-east","ingest_url":"u"}`))
	}))
	defer srv.Close()

	if _, err := newTestClient(srv.URL).CreateTrail(context.Background(), TrailCreate{Name: "billing"}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestRateLimitRetryRules(t *testing.T) {
	tests := []struct {
		name         string
		retryAfter   string
		body         string
		wantRequests int32
	}{
		{"Retry-After 0 is retried", "0", ``, 3},
		{"quota_exceeded is not retried", "", `{"error":{"code":"quota_exceeded","message":"limit (5)","field":null}}`, 1},
		{"Retry-After beyond the backoff ceiling stops", "60", ``, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			_, err := newTestClient(srv.URL).ListTrails(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 429 {
				t.Fatalf("err = %v, want APIError 429", err)
			}
			if requests.Load() != tt.wantRequests {
				t.Fatalf("requests = %d, want %d", requests.Load(), tt.wantRequests)
			}
		})
	}
}

func TestRedirectIsAnErrorAndNeverFollowed(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).ListTrails(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 302 {
		t.Fatalf("err = %v, want APIError 302", err)
	}
	if followed.Load() {
		t.Fatal("the redirect target was requested")
	}
}

func TestRequestHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if _, err := newTestClient(srv.URL).ListTrails(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Api-Key k" {
		t.Errorf("Authorization = %q", got.Get("Authorization"))
	}
	if got.Get("User-Agent") != "santati-terraform/test" {
		t.Errorf("User-Agent = %q", got.Get("User-Agent"))
	}
	if got.Get("Content-Type") != "" {
		t.Errorf("a body-less request sent Content-Type %q", got.Get("Content-Type"))
	}
}

func TestDeleteTrailPathAndRegion(t *testing.T) {
	var uri, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri, method = r.RequestURI, r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL + "/")
	if err := c.DeleteTrail(context.Background(), "bil ling", "eu-west"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || uri != "/api/v0/trails/bil%20ling/?region=eu-west" {
		t.Fatalf("%s %s", method, uri)
	}
	if err := c.DeleteTrail(context.Background(), "billing", ""); err != nil {
		t.Fatal(err)
	}
	if uri != "/api/v0/trails/billing/" {
		t.Fatalf("uri = %s: an empty region must not be sent", uri)
	}
}

func TestErrorBodies(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		code    string
		message string
	}{
		{
			name: "field errors", status: 400,
			body: `{"name":["A log stream with that name already exists."]}`,
			want: "HTTP 400: name: A log stream with that name already exists.",
		},
		{
			name: "error envelope", status: 400,
			body: `{"error":{"code":"quota_exceeded","message":"This team has reached its log stream limit (5).","field":null}}`,
			want: "HTTP 400 quota_exceeded: This team has reached its log stream limit (5).",
			code: "quota_exceeded",
		},
		{
			name: "error envelope with a field", status: 422,
			body: `{"error":{"code":"invalid_value","message":"must be positive","field":"timeout"}}`,
			want: "HTTP 422 invalid_value: timeout: must be positive",
			code: "invalid_value",
		},
		{
			name: "detail", status: 409,
			body: `{"detail":"source already exists (409): {'error': 'source_exists'}"}`,
			want: "HTTP 409: source already exists (409): {'error': 'source_exists'}",
		},
		{
			name: "multiple fields are sorted and joined", status: 400,
			body: `{"trail":["Unknown trail.","Pick another."],"config":{"url":["Enter a valid URL."]}}`,
			want: "HTTP 400: config.url: Enter a valid URL.; trail: Unknown trail. Pick another.",
		},
		{
			name: "an unrecognised object falls back to the status", status: 500,
			body: `{"unexpected":1}`,
			want: "HTTP 500: HTTP 500",
		},
		{
			name: "an empty body falls back to the status", status: 502,
			body: ``,
			want: "HTTP 502: HTTP 502",
		},
		{
			name: "an envelope without a code is not an envelope", status: 400,
			body: `{"error":{"message":"x"}}`,
			want: "HTTP 400: HTTP 400",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := newTestClient(srv.URL)
			c.maxRetries = 0
			_, err := c.ListTrails(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want APIError", err)
			}
			if apiErr.Error() != tt.want {
				t.Errorf("Error() = %q, want %q", apiErr.Error(), tt.want)
			}
			if apiErr.Code != tt.code {
				t.Errorf("Code = %q, want %q", apiErr.Code, tt.code)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found."}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).GetStream(context.Background(), 7)
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}
	if IsNotFound(errors.New("HTTP 404")) {
		t.Fatal("IsNotFound matched a plain error")
	}
}

func TestHTTPSConfigDecoding(t *testing.T) {
	tests := []struct {
		name string
		json string
		want HTTPSConfig
	}{
		{
			name: "dashboard shape: strings for everything",
			json: `{"url":"https://x","content_type":"","headers":"X-A: b\nX-C: d","timeout_seconds":""}`,
			want: HTTPSConfig{URL: "https://x", ContentType: "json", Headers: map[string]string{"X-A": "b", "X-C": "d"}, TimeoutSeconds: 15},
		},
		{
			name: "API shape: object headers and a number",
			json: `{"url":"https://x","content_type":"ndjson","headers":{"X-A":"b"},"timeout_seconds":30}`,
			want: HTTPSConfig{URL: "https://x", ContentType: "ndjson", Headers: map[string]string{"X-A": "b"}, TimeoutSeconds: 30},
		},
		{
			name: "a numeric string timeout",
			json: `{"url":"https://x","timeout_seconds":" 30 "}`,
			want: HTTPSConfig{URL: "https://x", ContentType: "json", TimeoutSeconds: 30},
		},
		{
			name: "content type is trimmed and lower-cased",
			json: `{"url":"https://x","content_type":" NDJSON ","timeout_seconds":0}`,
			want: HTTPSConfig{URL: "https://x", ContentType: "ndjson", TimeoutSeconds: 15},
		},
		{
			name: "header lines: blank lines skipped, split at the first colon",
			json: `{"url":"https://x","headers":"\nX-A: https://a:80\r\n\n  X-B :c  "}`,
			want: HTTPSConfig{URL: "https://x", ContentType: "json", Headers: map[string]string{"X-A": "https://a:80", "X-B": "c"}, TimeoutSeconds: 15},
		},
		{
			name: "null header values become empty strings",
			json: `{"url":"https://x","headers":{"X-A":null}}`,
			want: HTTPSConfig{URL: "https://x", ContentType: "json", Headers: map[string]string{"X-A": ""}, TimeoutSeconds: 15},
		},
		{
			name: "empty headers are none",
			json: `{"url":"https://x","headers":""}`,
			want: HTTPSConfig{URL: "https://x", ContentType: "json", TimeoutSeconds: 15},
		},
		{
			name: "null config is all defaults",
			json: `null`,
			want: HTTPSConfig{ContentType: "json", TimeoutSeconds: 15},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got HTTPSConfig
			if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}

	var bad HTTPSConfig
	if err := json.Unmarshal([]byte(`{"url":"https://x","timeout_seconds":"soon"}`), &bad); err == nil {
		t.Fatal("a non-numeric timeout string decoded without error")
	}
}

func TestStreamWriteEncoding(t *testing.T) {
	config := HTTPSConfig{URL: "https://x", ContentType: "json", TimeoutSeconds: 15}
	tests := []struct {
		name string
		in   StreamWrite
		want string
	}{
		{
			name: "nil auth is omitted, empty match rules are {}",
			in:   StreamWrite{Name: "s", Trail: "t", DestinationType: "https", Config: config, Status: "active"},
			want: `{"name":"s","trail":"t","destination_type":"https","config":{"url":"https://x","content_type":"json","timeout_seconds":15},"match_rules":{},"status":"active"}`,
		},
		{
			name: "an empty auth is {} and clears the secret",
			in:   StreamWrite{Name: "s", Trail: "t", DestinationType: "https", Config: config, Auth: &HTTPSAuth{}, Status: "active"},
			want: `{"name":"s","trail":"t","destination_type":"https","config":{"url":"https://x","content_type":"json","timeout_seconds":15},"auth":{},"match_rules":{},"status":"active"}`,
		},
		{
			name: "match rules carry only what is set",
			in: StreamWrite{
				Name: "s", Trail: "t", DestinationType: "https", Config: config, Status: "inactive",
				Auth:       &HTTPSAuth{HeaderName: "X-Key", HeaderValue: "v"},
				MatchRules: MatchRules{Actions: []string{"invoice.*"}, Metadata: []MatchClause{{Key: "region", Op: "eq", Value: "us-east"}}},
			},
			want: `{"name":"s","trail":"t","destination_type":"https","config":{"url":"https://x","content_type":"json","timeout_seconds":15},"auth":{"header_name":"X-Key","header_value":"v"},"match_rules":{"actions":["invoice.*"],"metadata":[{"key":"region","op":"eq","value":"us-east"}]},"status":"inactive"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestListFollowsEveryPage(t *testing.T) {
	var cursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/organizations/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		switch cursor {
		case "":
			// `next` names a host the client must not follow: only its cursor counts.
			_, _ = w.Write([]byte(`{"next":"https://elsewhere.invalid/api/v0/organizations/?cursor=c2","previous":null,"results":[{"id":1,"external_id":"a"},{"id":2,"external_id":"b"}]}`))
		case "c2":
			_, _ = w.Write([]byte(`{"next":null,"previous":null,"results":[{"id":3,"external_id":"c"}]}`))
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	}))
	defer srv.Close()

	orgs, err := newTestClient(srv.URL).ListOrganizations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 3 || orgs[0].ExternalID != "a" || orgs[2].ID != 3 {
		t.Fatalf("orgs = %+v", orgs)
	}
	if !reflect.DeepEqual(cursors, []string{"", "c2"}) {
		t.Fatalf("cursors = %q", cursors)
	}
}

func TestListRejectsACursorLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"next":"/api/v0/event-definitions/?cursor=same","results":[]}`))
	}))
	defer srv.Close()

	if _, err := newTestClient(srv.URL).ListEventDefinitions(context.Background()); err == nil {
		t.Fatal("a repeating cursor looped forever or succeeded")
	}
}

func TestContextCancelStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).ListTrails(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSchemaVersionRequests(t *testing.T) {
	type request struct{ line, contentType, body string }
	var (
		mu       sync.Mutex
		requests []request
	)
	const version = `{"version":2,"status":"draft","schema":{"type":"object"},"published_at":null,"deprecated_at":null,"deprecation_deadline":null,"created_at":"c","updated_at":"u"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, request{r.Method + " " + r.RequestURI, r.Header.Get("Content-Type"), string(body)})
		mu.Unlock()
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.RequestURI == "/api/v0/event-definitions/a.b/schema-versions/":
			_, _ = w.Write([]byte(`{"next":null,"previous":null,"results":[` + version + `]}`))
		default:
			_, _ = w.Write([]byte(version))
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c := newTestClient(srv.URL)
	doc := json.RawMessage(`{"type":"object"}`)
	if _, err := c.CreateSchemaVersion(ctx, "a.b", doc); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetSchemaVersion(ctx, "a.b", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateSchemaVersion(ctx, "a.b", 2, doc); err != nil {
		t.Fatal(err)
	}
	published, err := c.PublishSchemaVersion(ctx, "a.b", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSchemaVersion(ctx, "a.b", 2); err != nil {
		t.Fatal(err)
	}
	listed, err := c.ListSchemaVersions(ctx, "a.b")
	if err != nil {
		t.Fatal(err)
	}

	const base = "/api/v0/event-definitions/a.b/schema-versions/"
	want := []request{
		{"POST " + base, "application/json", `{"schema":{"type":"object"}}`},
		{"GET " + base + "2/", "", ""},
		{"PUT " + base + "2/", "application/json", `{"schema":{"type":"object"}}`},
		{"POST " + base + "2/publish/", "", ""},
		{"DELETE " + base + "2/", "", ""},
		{"GET " + base, "", ""},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %+v\nwant     %+v", requests, want)
	}
	if published.Version != 2 || published.Status != "draft" || string(published.Schema) != `{"type":"object"}` || published.PublishedAt != nil {
		t.Fatalf("published = %+v", published)
	}
	if len(listed) != 1 || listed[0].Version != 2 {
		t.Fatalf("listed = %+v", listed)
	}
}
