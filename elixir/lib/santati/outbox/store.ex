defmodule Santati.Outbox.Store do
  @moduledoc """
  The behaviour of an outbox store: where `Santati.Outbox` keeps logged events
  until the worker has sent them.

  The server owns the store's state and threads it through every call, so a
  callback may be a pure function of its state. A stored event is the wire
  envelope as a string-keyed map. Failures are `{:error, exception}`; a store
  answers `%Santati.OutboxError{}` itself, any other exception is wrapped as
  `store_unavailable` by the server.

    * `init/1` — builds the initial state from the store's options
    * `enqueue/2` — stores an event at the tail; `outbox_full` when bounded
    * `claim/2` — up to `n` oldest entries, FIFO; a claimed entry is not
      returned by another claim until released (Redis: until stale); `claim(0)`
      answers `[]`
    * `ack/2` — deletes entries permanently
    * `release/2` — makes entries eligible for a later claim
  """

  alias Santati.OutboxEntry

  @type state :: term()

  @callback init(opts :: keyword()) :: {:ok, state()} | {:error, Exception.t()}
  @callback enqueue(state(), event :: map()) :: {:ok, state()} | {:error, Exception.t()}
  @callback claim(state(), n :: non_neg_integer()) ::
              {:ok, [OutboxEntry.t()], state()} | {:error, Exception.t()}
  @callback ack(state(), ids :: [String.t()]) :: {:ok, state()} | {:error, Exception.t()}
  @callback release(state(), ids :: [String.t()]) :: {:ok, state()} | {:error, Exception.t()}
end
