# frozen_string_literal: true

module Santati
  # `emit`'s answer: the stored event (nil when queued), whether the server
  # replayed an earlier request, the idempotency key the request carried, and
  # whether the event went to the outbox instead of the API.
  EmitResult = Data.define(:event, :duplicate, :idempotency_key, :queued)

  # One rejected batch item's error.
  BatchItemError = Data.define(:code, :message, :field)

  # One batch item's result.
  BatchItem = Data.define(:index, :status, :id, :error)

  # `emit_batch`'s answer: per-item results, in submission order.
  BatchResult = Data.define(:accepted, :rejected, :results)

  # One page of `list`: the events and the cursor for the next page, if any.
  EventPage = Data.define(:results, :next_cursor)
end
