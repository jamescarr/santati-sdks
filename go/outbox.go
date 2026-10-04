package santati

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// MemoryOutbox is an in-process OutboxStore: a bounded FIFO queue held in
// memory, safe for concurrent use. Events are lost when the process exits;
// use the redisoutbox package for a durable store.
type MemoryOutbox struct {
	mu         sync.Mutex
	maxPending int
	pending    []OutboxEntry
	claimed    map[string]OutboxEntry
	nextID     int
}

var _ OutboxStore = (*MemoryOutbox)(nil)

// NewMemoryOutbox returns a store that holds at most maxPending events
// (pending plus claimed). It returns a *Error with kind KindValidation and
// field "max_pending" when maxPending is less than 1.
func NewMemoryOutbox(maxPending int) (*MemoryOutbox, error) {
	if maxPending < 1 {
		return nil, validationError("max_pending", "max_pending must be at least 1")
	}
	return &MemoryOutbox{maxPending: maxPending, claimed: map[string]OutboxEntry{}, nextID: 1}, nil
}

// Enqueue stores the event, or fails with OutboxError "outbox_full".
func (m *MemoryOutbox) Enqueue(_ context.Context, event EventInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending)+len(m.claimed) >= m.maxPending {
		return outboxError("outbox_full", fmt.Sprintf("outbox is full (%d events)", m.maxPending))
	}
	m.pending = append(m.pending, OutboxEntry{ID: strconv.Itoa(m.nextID), Event: event})
	m.nextID++
	return nil
}

// Claim moves up to limit of the oldest pending entries to the claimed set.
func (m *MemoryOutbox) Claim(_ context.Context, limit int) ([]OutboxEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := min(max(limit, 0), len(m.pending))
	entries := make([]OutboxEntry, n)
	copy(entries, m.pending[:n])
	m.pending = append([]OutboxEntry(nil), m.pending[n:]...)
	for _, entry := range entries {
		m.claimed[entry.ID] = entry
	}
	return entries, nil
}

// Ack deletes claimed entries.
func (m *MemoryOutbox) Ack(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.claimed, id)
	}
	return nil
}

// Release moves claimed entries back to the front of the queue, keeping their
// relative order.
func (m *MemoryOutbox) Release(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var released []OutboxEntry
	for _, id := range ids {
		if entry, ok := m.claimed[id]; ok {
			delete(m.claimed, id)
			released = append(released, entry)
		}
	}
	m.pending = append(released, m.pending...)
	return nil
}

// outbox is the per-client outbox state: the store, the hooks and the worker.
type outbox struct {
	client    *Client
	store     OutboxStore
	batchSize int
	interval  time.Duration
	preSend   PreSendHook
	postSend  PostSendHook

	// pass serialises passes: the worker's ticks and Flush never overlap.
	pass sync.Mutex

	// life guards started/closed. enqueue holds it for reading across the
	// store write so Close waits for in-flight emits before the final flush.
	life    sync.RWMutex
	started atomic.Bool
	closed  bool
	stop    chan struct{}
	done    chan struct{}
}

// enqueue stores the already-resolved event in the store. It never makes a
// request; a background worker sends the event later, and the first call
// starts the worker.
//
// Failures of the store are a *Error with kind KindOutbox: code "outbox_full"
// for a full MemoryOutbox, "store_unavailable" for any other store failure and
// "closed" after Close.
func (o *outbox) enqueue(ctx context.Context, stored EventInput) error {
	o.life.RLock()
	if o.closed {
		o.life.RUnlock()
		return outboxError("closed", "client is closed")
	}
	err := o.store.Enqueue(ctx, stored)
	o.life.RUnlock()
	if err != nil {
		return storeError(err)
	}
	o.start()
	return nil
}

// Flush runs one outbox pass synchronously: it sends everything pending in
// batches of the configured size, stopping early after a retryable failure.
// It returns an OutboxError "store_unavailable" if the store fails during the
// pass; send failures never fail Flush, they are reported to the PostSendHook.
// Without an outbox, Flush returns nil at once.
func (c *Client) Flush(ctx context.Context) error {
	if c.outbox == nil {
		return nil
	}
	return c.outbox.flush(ctx)
}

// Close stops the outbox worker, waits for a pass in progress, then runs a
// final Flush. It is idempotent: later calls return nil without another pass.
// After Close, a queued Emit fails with OutboxError "closed". Without an
// outbox, Close returns nil and Emit keeps working.
func (c *Client) Close(ctx context.Context) error {
	o := c.outbox
	if o == nil {
		return nil
	}
	o.life.Lock()
	if o.closed {
		o.life.Unlock()
		return nil
	}
	o.closed = true
	started := o.started.Load()
	o.life.Unlock()

	if started {
		close(o.stop)
		<-o.done
	}
	return o.flush(ctx)
}

func (o *outbox) start() {
	if o.started.Load() {
		return
	}
	o.life.Lock()
	defer o.life.Unlock()
	if o.started.Load() || o.closed {
		return
	}
	o.started.Store(true)
	o.stop = make(chan struct{})
	o.done = make(chan struct{})
	go o.run(o.stop, o.done)
}

func (o *outbox) run(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	timer := time.NewTimer(o.interval)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			o.tick()
			timer.Reset(o.interval)
		}
	}
}

// tick runs one worker pass. Store failures are swallowed (the next tick
// retries) and a panic must not kill the worker.
func (o *outbox) tick() {
	defer func() { _ = recover() }()
	_ = o.flush(context.Background())
}

func (o *outbox) flush(ctx context.Context) error {
	o.pass.Lock()
	defer o.pass.Unlock()
	return o.runPass(ctx)
}

type pendingSend struct {
	entry OutboxEntry
	out   EventInput
}

func (o *outbox) runPass(ctx context.Context) error {
	for {
		entries, err := o.store.Claim(ctx, o.batchSize)
		if err != nil {
			return storeError(err)
		}
		if len(entries) == 0 {
			return nil
		}

		released := false
		toSend := make([]pendingSend, 0, len(entries))
		for _, entry := range entries {
			out := entry.Event
			if o.preSend != nil {
				result, keep, hookErr := o.callPreSend(cloneEvent(entry.Event))
				if hookErr != nil {
					if err := o.store.Release(ctx, []string{entry.ID}); err != nil {
						return storeError(err)
					}
					released = true
					o.callPostSend(entry.Event, SendOutcome{Status: SendFailed, Err: hookErr})
					continue
				}
				if !keep {
					if err := o.store.Ack(ctx, []string{entry.ID}); err != nil {
						return storeError(err)
					}
					continue
				}
				out = result
			}
			toSend = append(toSend, pendingSend{entry: entry, out: out})
		}

		if len(toSend) > 0 {
			retry, err := o.send(ctx, toSend)
			if err != nil {
				return err
			}
			released = released || retry
		}
		if released || len(entries) < o.batchSize {
			return nil
		}
	}
}

// send emits one batch, reports the outcomes and acks or releases the
// entries. retry is true when the batch was released for a later pass.
func (o *outbox) send(ctx context.Context, toSend []pendingSend) (retry bool, err error) {
	ids := make([]string, len(toSend))
	outs := make([]EventInput, len(toSend))
	for i, item := range toSend {
		ids[i] = item.entry.ID
		outs[i] = item.out
	}

	result, sendErr := o.client.Events.EmitBatch(ctx, outs)
	if sendErr != nil {
		var sdkErr *Error
		if !errors.As(sendErr, &sdkErr) {
			sdkErr = transportError(sendErr)
		}
		for _, item := range toSend {
			o.callPostSend(item.entry.Event, SendOutcome{Status: SendFailed, Err: sdkErr})
		}
		switch sdkErr.Kind {
		case KindTransport, KindServer, KindRateLimited:
			if err := o.store.Release(ctx, ids); err != nil {
				return false, storeError(err)
			}
			return true, nil
		default:
			if err := o.store.Ack(ctx, ids); err != nil {
				return false, storeError(err)
			}
			return false, nil
		}
	}

	for _, item := range result.Results {
		if item.Index < 0 || item.Index >= len(toSend) {
			continue
		}
		o.callPostSend(toSend[item.Index].entry.Event, outcomeFor(item, result.status))
	}
	if err := o.store.Ack(ctx, ids); err != nil {
		return false, storeError(err)
	}
	return false, nil
}

func outcomeFor(item BatchItem, status int) SendOutcome {
	if item.Status == string(SendRejected) {
		outcome := SendOutcome{Status: SendRejected, Err: &Error{Kind: KindValidation, Status: status}}
		if item.Error != nil {
			outcome.Err.Code = item.Error.Code
			outcome.Err.Field = item.Error.Field
			outcome.Err.Message = item.Error.Message
		}
		return outcome
	}
	return SendOutcome{Status: SendStatus(item.Status), ID: item.ID}
}

// cloneEvent deep-copies the reference members of an event so neither the
// caller of a queued Emit nor a hook that mutates its argument in place can change the
// stored original.
func cloneEvent(e EventInput) EventInput {
	if e.Actor != nil {
		actor := *e.Actor
		actor.Metadata = maps.Clone(actor.Metadata)
		e.Actor = &actor
	}
	if e.Targets != nil {
		targets := make([]TargetInput, len(e.Targets))
		for i, target := range e.Targets {
			target.Metadata = maps.Clone(target.Metadata)
			targets[i] = target
		}
		e.Targets = targets
	}
	e.Metadata = maps.Clone(e.Metadata)
	if e.Context != nil {
		e.Context = cloneAny(e.Context).(map[string]any)
	}
	e.Data = cloneAny(e.Data)
	return e
}

func cloneAny(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneAny(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	case map[string]string:
		return maps.Clone(typed)
	default:
		return v
	}
}

// callPreSend runs the hook, converting a panic into an OutboxError.
func (o *outbox) callPreSend(event EventInput) (out EventInput, keep bool, failure *Error) {
	defer func() {
		if r := recover(); r != nil {
			failure = outboxError("hook_failed", fmt.Sprint(r))
		}
	}()
	out, keep = o.preSend(event)
	return out, keep, nil
}

// callPostSend runs the hook, ignoring a panic.
func (o *outbox) callPostSend(event EventInput, outcome SendOutcome) {
	if o.postSend == nil {
		return
	}
	defer func() { _ = recover() }()
	o.postSend(event, outcome)
}

// storeError reports a store failure as an *Error: a store's own *Error passes
// through, anything else becomes OutboxError "store_unavailable".
func storeError(err error) *Error {
	var sdkErr *Error
	if errors.As(err, &sdkErr) {
		return sdkErr
	}
	return outboxError("store_unavailable", err.Error())
}
