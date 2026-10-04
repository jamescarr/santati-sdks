package santati_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

// Close gives up when its context ends, although a send is in flight and the
// client's own timeout is far longer.
func TestCloseHonoursContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // net/http notices a disconnect only once the body is read
		<-r.Context().Done()
	}))
	defer server.Close()

	store, err := santati.NewMemoryOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	client, err := santati.NewClient("sat_sk_x",
		santati.WithBaseURL(server.URL), santati.WithTrail("t"), santati.WithOutbox(store),
		santati.WithTimeout(30*time.Second), santati.WithFlushInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Events.Emit(context.Background(), santati.EventInput{Event: "a.b"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the worker's send is now in flight

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = client.Close(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close took %v with a 300ms context", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want context.DeadlineExceeded", err)
	}
}

// ctxStore is a store that, like a database-backed one, fails Ack and Release
// when the context it is handed has ended.
type ctxStore struct{ *santati.MemoryOutbox }

func (s ctxStore) Ack(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.MemoryOutbox.Ack(ctx, ids)
}

func (s ctxStore) Release(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.MemoryOutbox.Release(ctx, ids)
}

// Close cancels the worker's send in flight. The batch it aborted must go back
// to a store that honours its context, so the final flush sends it.
func TestCloseResendsTheAbortedBatchToAContextAwareStore(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // net/http notices a disconnect only once the body is read
		if requests.Add(1) == 1 {
			<-r.Context().Done() // the worker's send hangs until Close cancels it
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":0,"results":[{"index":0,"status":"accepted","id":"x"}]}`))
	}))
	defer server.Close()

	memory, err := santati.NewMemoryOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	client, err := santati.NewClient("sat_sk_x",
		santati.WithBaseURL(server.URL), santati.WithTrail("t"), santati.WithOutbox(ctxStore{memory}),
		santati.WithTimeout(30*time.Second), santati.WithFlushInterval(10*time.Millisecond),
		santati.WithPostSend(func(_ santati.EventInput, o santati.SendOutcome) {
			if o.Status == santati.SendAccepted {
				accepted.Add(1)
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Events.Emit(context.Background(), santati.EventInput{Event: "a.b"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the worker's send is now in flight

	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if got := accepted.Load(); got != 1 {
		t.Fatalf("the aborted batch was sent %d times by the final flush, want 1", got)
	}
}
