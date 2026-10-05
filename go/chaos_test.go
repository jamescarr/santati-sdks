//go:build chaos

// Chaos driver: emits into the outbox against the gateway in SANTATI_CHAOS and
// prints one CHAOS_RESULT line. Run by `mise run chaos go` (chaos/run.mjs);
// built only with -tags chaos and inert unless SANTATI_CHAOS is set.
package santati_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	santati "github.com/jamescarr/santati-sdks/go"
)

type chaosConfig struct {
	Client struct {
		BaseURL          string `json:"base_url"`
		TimeoutMS        int    `json:"timeout_ms"`
		MaxRetries       int    `json:"max_retries"`
		InitialBackoffMS int    `json:"initial_backoff_ms"`
		MaxBackoffMS     int    `json:"max_backoff_ms"`
		BatchSize        int    `json:"batch_size"`
		FlushIntervalMS  int    `json:"flush_interval_ms"`
		MaxPending       int    `json:"max_pending"`
	} `json:"client"`
	Emitters          int `json:"emitters"`
	EventsPerEmitter  int `json:"events_per_emitter"`
	EmitIntervalMS    int `json:"emit_interval_ms"`
	FlusherIntervalMS int `json:"flusher_interval_ms"`
	HeartbeatMS       int `json:"heartbeat_ms"`
}

func chaosMS(d time.Duration) float64 {
	return math.Round(float64(d)/float64(time.Millisecond)*1000) / 1000
}

func chaosPercentile(sorted []time.Duration, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return chaosMS(sorted[idx])
}

func chaosErrorKey(err error) string {
	var sdkErr *santati.Error
	if errors.As(err, &sdkErr) {
		return fmt.Sprintf("%s:%s", sdkErr.Kind, sdkErr.Code)
	}
	return fmt.Sprintf("%T:unexpected", err)
}

func TestChaos(t *testing.T) {
	raw := os.Getenv("SANTATI_CHAOS")
	if raw == "" {
		fmt.Println("SANTATI_CHAOS not set; skipping")
		return
	}
	var cfg chaosConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }

	store, err := santati.NewMemoryOutbox(cfg.Client.MaxPending)
	if err != nil {
		t.Fatal(err)
	}
	client, err := santati.NewClient("sat_sk_chaos",
		santati.WithBaseURL(cfg.Client.BaseURL),
		santati.WithTrail("chaos"),
		santati.WithTimeout(ms(cfg.Client.TimeoutMS)),
		santati.WithMaxRetries(cfg.Client.MaxRetries),
		santati.WithBackoff(ms(cfg.Client.InitialBackoffMS), ms(cfg.Client.MaxBackoffMS)),
		santati.WithBatchSize(cfg.Client.BatchSize),
		santati.WithFlushInterval(ms(cfg.Client.FlushIntervalMS)),
		santati.WithOutbox(store),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Heartbeat: how late a 10 ms sleep wakes up.
	var maxLag atomic.Int64
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		beat := ms(cfg.HeartbeatMS)
		for {
			start := time.Now()
			select {
			case <-stopHeartbeat:
				return
			case <-time.After(beat):
			}
			lag := int64(time.Since(start) - beat)
			for {
				cur := maxLag.Load()
				if lag <= cur || maxLag.CompareAndSwap(cur, lag) {
					break
				}
			}
		}
	}()

	// Flusher.
	var flushCalls atomic.Int64
	stopFlusher := make(chan struct{})
	flusherDone := make(chan struct{})
	if cfg.FlusherIntervalMS > 0 {
		go func() {
			defer close(flusherDone)
			ticker := time.NewTicker(ms(cfg.FlusherIntervalMS))
			defer ticker.Stop()
			for {
				select {
				case <-stopFlusher:
					return
				case <-ticker.C:
					flushCalls.Add(1)
					_ = client.Flush(context.Background())
				}
			}
		}()
	} else {
		close(flusherDone)
	}

	// Emitters.
	var mu sync.Mutex
	var latencies []time.Duration
	errorCounts := map[string]int{}
	var queued, crashes atomic.Int64
	var wg sync.WaitGroup
	for i := range cfg.Emitters {
		wg.Add(1)
		go func(emitter int) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					crashes.Add(1)
				}
			}()
			local := make([]time.Duration, 0, cfg.EventsPerEmitter)
			localErrors := map[string]int{}
			for seq := range cfg.EventsPerEmitter {
				start := time.Now()
				result, err := client.Events.Emit(context.Background(), santati.EventInput{
					Event:    "chaos.event",
					Metadata: map[string]string{"emitter": strconv.Itoa(emitter), "seq": strconv.Itoa(seq)},
				})
				local = append(local, time.Since(start))
				if err != nil {
					localErrors[chaosErrorKey(err)]++
				} else if result.Queued {
					queued.Add(1)
				}
				if cfg.EmitIntervalMS > 0 {
					time.Sleep(ms(cfg.EmitIntervalMS))
				}
			}
			mu.Lock()
			latencies = append(latencies, local...)
			for key, n := range localErrors {
				errorCounts[key] += n
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	close(stopFlusher)
	<-flusherDone

	closeStart := time.Now()
	if err := client.Close(context.Background()); err != nil {
		errorCounts[chaosErrorKey(err)]++
	}
	closeElapsed := time.Since(closeStart)
	close(stopHeartbeat)
	<-heartbeatDone

	sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })
	var maxEmit time.Duration
	if len(latencies) > 0 {
		maxEmit = latencies[len(latencies)-1]
	}
	out, err := json.Marshal(map[string]any{
		"sdk":          "go",
		"emits":        len(latencies),
		"queued":       queued.Load(),
		"errors":       errorCounts,
		"caller_exits": crashes.Load(),
		"emit_ms": map[string]float64{
			"p50": chaosPercentile(latencies, 0.50),
			"p99": chaosPercentile(latencies, 0.99),
			"max": chaosMS(maxEmit),
		},
		"heartbeat_max_lag_ms": chaosMS(time.Duration(maxLag.Load())),
		"close_ms":             chaosMS(closeElapsed),
		"flush_calls":          flushCalls.Load(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("CHAOS_RESULT " + string(out))
}
