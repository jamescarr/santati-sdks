// Package redisoutbox is a Redis-backed santati.OutboxStore.
//
// Events live in a Redis Stream read through one consumer group named
// "santati", so every SDK that implements the same layout can drain what
// another enqueued. The store uses the caller's go-redis client and never
// opens connections itself.
//
//	store := redisoutbox.New(redis.NewClient(&redis.Options{Addr: "localhost:6379"}))
//	client, err := santati.NewClient(apiKey, santati.WithOutbox(store))
package redisoutbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	santati "github.com/jamescarr/santati-sdks/go"
)

const (
	group           = "santati"
	defaultKey      = "santati:outbox"
	defaultVisible  = 60 * time.Second
	eventFieldName  = "event"
	uuidVersionByte = 6
	uuidVariantByte = 8
)

// Store is a santati.OutboxStore over a Redis Stream. It has no capacity bound.
type Store struct {
	client     redis.Cmdable
	key        string
	visibility time.Duration
	consumer   string

	initMu sync.Mutex
	ready  bool
}

var _ santati.OutboxStore = (*Store)(nil)

// Option configures a Store.
type Option func(*Store)

// WithKey sets the stream key. The default is "santati:outbox".
func WithKey(key string) Option {
	return func(s *Store) { s.key = key }
}

// WithVisibility sets how long an entry claimed by a consumer stays invisible
// to other claims; after that it is re-delivered. The default is 60s.
func WithVisibility(visibility time.Duration) Option {
	return func(s *Store) { s.visibility = visibility }
}

// New returns a store over the caller's Redis client.
func New(client redis.Cmdable, opts ...Option) *Store {
	s := &Store{
		client:     client,
		key:        defaultKey,
		visibility: defaultVisible,
		consumer:   newUUID(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func storeErr(err error) error {
	return &santati.Error{Kind: santati.KindOutbox, Code: "store_unavailable", Message: err.Error()}
}

func (s *Store) ensureGroup(ctx context.Context) error {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if s.ready {
		return nil
	}
	err := s.client.XGroupCreateMkStream(ctx, s.key, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return storeErr(err)
	}
	s.ready = true
	return nil
}

// Enqueue appends the event, JSON-encoded as the wire envelope, to the stream.
func (s *Store) Enqueue(ctx context.Context, event santati.EventInput) error {
	if err := s.ensureGroup(ctx); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return storeErr(err)
	}
	err = s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.key,
		Values: map[string]any{eventFieldName: string(encoded)},
	}).Err()
	if err != nil {
		return storeErr(err)
	}
	return nil
}

// Claim returns up to limit entries: first those another consumer left pending
// for at least the visibility timeout, then new ones. Entries that cannot be
// decoded are deleted.
func (s *Store) Claim(ctx context.Context, limit int) ([]santati.OutboxEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	if err := s.ensureGroup(ctx); err != nil {
		return nil, err
	}

	claimed, _, err := s.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   s.key,
		Group:    group,
		Consumer: s.consumer,
		MinIdle:  s.visibility,
		Start:    "0-0",
		Count:    int64(limit),
	}).Result()
	if err != nil && err != redis.Nil {
		return nil, storeErr(err)
	}
	messages := claimed
	if len(messages) < limit {
		streams, err := s.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: s.consumer,
			Streams:  []string{s.key, ">"},
			Count:    int64(limit - len(messages)),
			Block:    -1,
		}).Result()
		if err != nil && err != redis.Nil {
			return nil, storeErr(err)
		}
		for _, stream := range streams {
			messages = append(messages, stream.Messages...)
		}
	}

	entries := make([]santati.OutboxEntry, 0, len(messages))
	var poison []string
	for _, message := range messages {
		if message.ID == "" {
			continue // placeholder for a deleted entry
		}
		event, ok := decode(message.Values)
		if !ok {
			poison = append(poison, message.ID)
			continue
		}
		entries = append(entries, santati.OutboxEntry{ID: message.ID, Event: event})
	}
	if len(poison) > 0 {
		if err := s.Ack(ctx, poison); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func decode(values map[string]any) (santati.EventInput, bool) {
	var raw string
	switch value := values[eventFieldName].(type) {
	case string:
		raw = value
	case []byte:
		raw = string(value)
	default:
		return santati.EventInput{}, false
	}
	var event santati.EventInput
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return santati.EventInput{}, false
	}
	return event, true
}

// Ack removes the entries from the pending list and deletes them.
func (s *Store) Ack(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.client.XAck(ctx, s.key, group, ids...).Err(); err != nil {
		return storeErr(err)
	}
	if err := s.client.XDel(ctx, s.key, ids...).Err(); err != nil {
		return storeErr(err)
	}
	return nil
}

// Release does nothing: a released entry stays in the pending list and is
// re-delivered by Claim once the visibility timeout has passed.
func (s *Store) Release(context.Context, []string) error {
	return nil
}

// newUUID returns a lowercase random UUIDv4 naming this store's consumer.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[uuidVersionByte] = b[uuidVersionByte]&0x0f | 0x40
	b[uuidVariantByte] = b[uuidVariantByte]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
