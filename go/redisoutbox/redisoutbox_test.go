package redisoutbox_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/redis/go-redis/v9"

	santati "github.com/jamescarr/santati-sdks/go"
	"github.com/jamescarr/santati-sdks/go/redisoutbox"
)

func event(i int) santati.EventInput {
	return santati.EventInput{
		Event:          fmt.Sprintf("e%d", i),
		Trail:          "t",
		IdempotencyKey: fmt.Sprintf("k%d", i),
	}
}

func events(entries []santati.OutboxEntry) []santati.EventInput {
	out := make([]santati.EventInput, len(entries))
	for i, entry := range entries {
		out[i] = entry.Event
	}
	return out
}

func TestRedisOutbox(t *testing.T) {
	url := os.Getenv("SANTATI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SANTATI_TEST_REDIS_URL not set")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	ctx := context.Background()
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	key := fmt.Sprintf("santati:test:%x", suffix)
	t.Cleanup(func() {
		client.Del(ctx, key)
		_ = client.Close()
	})

	store := redisoutbox.New(client, redisoutbox.WithKey(key))
	for i := 1; i <= 3; i++ {
		if err := store.Enqueue(ctx, event(i)); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	first, err := store.Claim(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []santati.EventInput{event(1), event(2)}; !reflect.DeepEqual(events(first), want) {
		t.Fatalf("claim(2) = %+v, want %+v", events(first), want)
	}
	if first[0].ID == "" || first[1].ID == "" || first[0].ID == first[1].ID {
		t.Fatalf("ids must be non-empty and distinct: %q %q", first[0].ID, first[1].ID)
	}

	rest, err := store.Claim(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if want := []santati.EventInput{event(3)}; !reflect.DeepEqual(events(rest), want) {
		t.Fatalf("claim(5) = %+v, want %+v", events(rest), want)
	}
	again, err := store.Claim(ctx, 5)
	if err != nil || len(again) != 0 {
		t.Fatalf("claim(5) = %+v, %v, want empty", again, err)
	}

	if err := store.Ack(ctx, []string{first[0].ID, first[1].ID}); err != nil {
		t.Fatal(err)
	}

	// A second store with no visibility window sees the still-pending e3 and
	// nothing that was acked.
	other := redisoutbox.New(client, redisoutbox.WithKey(key), redisoutbox.WithVisibility(0))
	stale, err := other.Claim(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []santati.EventInput{event(3)}; !reflect.DeepEqual(events(stale), want) {
		t.Fatalf("stale claim = %+v, want %+v", events(stale), want)
	}

	if err := other.Ack(ctx, []string{rest[0].ID}); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*redisoutbox.Store{"first": store, "other": other} {
		got, err := s.Claim(ctx, 10)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s claim after ack = %+v, %v, want empty", name, got, err)
		}
	}
	if n, err := client.XLen(ctx, key).Result(); err != nil || n != 0 {
		t.Fatalf("XLEN = %d, %v, want 0", n, err)
	}
}
