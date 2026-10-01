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
// Errors from the API are always a *Error, whose Kind tells the caller which
// failure it was (validation, auth, not found, rate limited, server,
// transport or any other API error).
package santati
