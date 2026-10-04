// Package santati is the official Go SDK for the Santati audit-log API.
//
// A client talks to the control plane with a team API key and exposes one
// resource, Events, that emits single events or batches and lists or streams
// the audit events they produced:
//
//	client, err := santati.NewClient(os.Getenv("SANTATI_API_KEY"),
//		santati.WithTrail("billing"),
//	)
//	if err != nil {
//		return err
//	}
//
//	result, err := client.Events.Emit(ctx, santati.EventInput{
//		Event:          "invoice.voided",
//		OrganizationID: "org_acme",
//		Actor:          &santati.ActorInput{Type: "user", ID: "usr_123"},
//	})
//	if err != nil {
//		return err
//	}
//	_ = result
//
// Log is the fire-and-forget counterpart of Events.Emit: it validates the
// event, stores it in an outbox (in memory by default; the redisoutbox
// package offers a Redis-backed store) and returns its idempotency key. A
// background worker sends the outbox in batches, PreSend and PostSend hooks
// observe each event, and Flush or Close drain it synchronously:
//
//	key, err := client.Log(ctx, santati.EventInput{Event: "invoice.paid"})
//	...
//	err = client.Close(ctx)
//
// Errors from the API are always a *Error, whose Kind tells the caller which
// failure it was (validation, auth, not found, rate limited, server,
// transport or any other API error), or a KindOutbox error from the outbox.
package santati
