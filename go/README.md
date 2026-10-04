# santati (Go)

The official Go SDK for the Santati audit-log API.

## Install

```sh
go get github.com/jamescarr/santati-sdks/go
```

## Quickstart

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	santati "github.com/jamescarr/santati-sdks/go"
)

func main() {
	client, err := santati.NewClient(os.Getenv("SANTATI_API_KEY"),
		santati.WithTrail("billing"),
	)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Emit one event.
	result, err := client.Events.Emit(ctx, santati.EventInput{
		Event:          "invoice.voided",
		OrganizationID: "org_acme",
		Actor:          &santati.ActorInput{Type: "user", ID: "usr_123", Name: "Dana Ortiz"},
		Targets:        []santati.TargetInput{{Type: "invoice", ID: "inv_555"}},
		Data:           map[string]any{"amount_cents": 4200},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Event.Id, result.Duplicate)

	// Emit a batch; the server reports per-item results.
	batch, err := client.Events.EmitBatch(ctx, []santati.EventInput{
		{Event: "invoice.paid", OrganizationID: "org_acme"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(batch.Accepted, batch.Rejected)

	// List one page.
	page, err := client.Events.List(ctx, santati.ListParams{Trail: "billing", Limit: 50})
	if err != nil {
		log.Fatal(err)
	}
	for _, event := range page.Results {
		fmt.Println(event.Event)
	}

	// Iterate every page.
	for event, err := range client.Events.Iterate(ctx, santati.ListParams{Trail: "billing"}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(event.Id)
	}
}
```

## Outbox and `Log`

`Log` is a fire-and-forget emit: it validates the event like `Events.Emit`,
stores it in an outbox and returns the idempotency key without making a
request. A background worker sends the outbox in batches; `Close` stops it and
drains what is left.

```go
client, _ := santati.NewClient(os.Getenv("SANTATI_API_KEY"),
	santati.WithTrail("billing"),
	santati.WithPostSend(func(e santati.EventInput, o santati.SendOutcome) {
		log.Println(e.IdempotencyKey, o.Status)
	}),
)
defer client.Close(ctx)

key, err := client.Log(ctx, santati.EventInput{Event: "invoice.paid"})
```

The default store is an in-memory `MemoryOutbox` (10000 events). To survive
restarts, use the Redis adapter; it takes your existing go-redis client and
needs `go get github.com/redis/go-redis/v9`:

```go
import "github.com/jamescarr/santati-sdks/go/redisoutbox"

client, _ := santati.NewClient(key, santati.WithTrail("billing"),
	santati.WithOutbox(redisoutbox.New(redis.NewClient(&redis.Options{Addr: "localhost:6379"}))),
)
```

`NewClient` returns a `*santati.Error` for an empty API key or an
`Authorization` header passed through `WithHeader`. Every failure from the API
is a `*santati.Error` whose `Kind` is one of `KindValidation`, `KindAuth`,
`KindNotFound`, `KindRateLimited`, `KindServer`, `KindTransport` or `KindAPI`.
