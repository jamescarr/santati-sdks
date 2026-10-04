package santati_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	santati "github.com/jamescarr/santati-sdks/go"
)

// A pre_send hook that mutates its argument in place must not change the
// stored event post_send receives.
func TestPostSendSeesOriginalAfterPreSendMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":0,"results":[{"index":0,"status":"accepted","id":"x"}]}`))
	}))
	defer server.Close()

	var seen santati.EventInput
	store, err := santati.NewMemoryOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	client, err := santati.NewClient("sat_sk_x",
		santati.WithBaseURL(server.URL), santati.WithTrail("t"), santati.WithOutbox(store),
		santati.WithPreSend(func(e santati.EventInput) (santati.EventInput, bool) {
			e.Metadata["region"] = "eu"
			e.Actor.Metadata["k"] = "v"
			return e, true
		}),
		santati.WithPostSend(func(e santati.EventInput, _ santati.SendOutcome) { seen = e }),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = client.Events.Emit(ctx, santati.EventInput{
		Event:    "a.b",
		Metadata: map[string]string{},
		Actor:    &santati.ActorInput{Type: "user", ID: "u", Metadata: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(seen.Metadata) != 0 || len(seen.Actor.Metadata) != 0 {
		t.Fatalf("post_send saw a mutated event: %+v actor %+v", seen.Metadata, seen.Actor.Metadata)
	}
}

// Changing the caller's maps after a queued Emit must not change the stored event.
func TestQueuedEmitSnapshotsTheEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":0,"results":[{"index":0,"status":"accepted","id":"x"}]}`))
	}))
	defer server.Close()

	var seen santati.EventInput
	store, err := santati.NewMemoryOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	client, err := santati.NewClient("sat_sk_x",
		santati.WithBaseURL(server.URL), santati.WithTrail("t"), santati.WithOutbox(store),
		santati.WithPostSend(func(e santati.EventInput, _ santati.SendOutcome) { seen = e }),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := map[string]string{"a": "1"}
	if _, err := client.Events.Emit(ctx, santati.EventInput{Event: "a.b", Metadata: m}); err != nil {
		t.Fatal(err)
	}
	m["a"] = "2"
	if err := client.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if seen.Metadata["a"] != "1" {
		t.Fatalf("the stored event changed with the caller's map: %+v", seen.Metadata)
	}
}
