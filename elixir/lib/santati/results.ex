defmodule Santati.EmitResult do
  @moduledoc """
  One accepted (or replayed) emit.

    * `event` — the stored `SantatiCore.Model.AuditEvent` (`nil` when queued)
    * `duplicate` — `true` when the server replayed an earlier request
    * `idempotency_key` — the key the envelope carried
    * `queued` — `true` when the client's `:outbox` stored the event instead of
      sending it
  """

  @derive JSON.Encoder
  defstruct [:event, :duplicate, :idempotency_key, queued: false]

  @type t :: %__MODULE__{
          event: SantatiCore.Model.AuditEvent.t() | nil,
          duplicate: boolean(),
          idempotency_key: String.t(),
          queued: boolean()
        }
end

defmodule Santati.BatchItemError do
  @moduledoc "The server's rejection of one batch item."

  @derive JSON.Encoder
  defstruct [:code, :message, :field]

  @type t :: %__MODULE__{
          code: String.t(),
          message: String.t(),
          field: String.t() | nil
        }
end

defmodule Santati.BatchItem do
  @moduledoc "The result of one event in a batch."

  @derive JSON.Encoder
  defstruct [:index, :status, :id, :error]

  @type t :: %__MODULE__{
          index: non_neg_integer(),
          status: String.t(),
          id: String.t() | nil,
          error: Santati.BatchItemError.t() | nil
        }
end

defmodule Santati.BatchResult do
  @moduledoc "The result of a batch emit."

  @derive JSON.Encoder
  defstruct [:accepted, :rejected, :results]

  @type t :: %__MODULE__{
          accepted: non_neg_integer(),
          rejected: non_neg_integer(),
          results: [Santati.BatchItem.t()]
        }
end

defmodule Santati.EventPage do
  @moduledoc """
  One page of audit events.

    * `results` — the page's `SantatiCore.Model.AuditEvent` structs
    * `next_cursor` — the cursor of the next page, or `nil` on the last page
  """

  @derive JSON.Encoder
  defstruct [:results, :next_cursor]

  @type t :: %__MODULE__{
          results: [SantatiCore.Model.AuditEvent.t()],
          next_cursor: String.t() | nil
        }
end

defmodule Santati.OutboxEntry do
  @moduledoc "One claimed outbox entry: the store's `id` and the stored `event` map."

  defstruct [:id, :event]

  @type t :: %__MODULE__{id: String.t(), event: map()}
end

defmodule Santati.SendOutcome do
  @moduledoc """
  What happened to one event in a pass, handed to `post_send`.

    * `status` — `:accepted`, `:duplicate`, `:rejected` or `:failed`
    * `id` — the stored event's id for `:accepted` and `:duplicate`
    * `error` — the exception for `:rejected` and `:failed`
  """

  defstruct [:status, :id, :error]

  @type t :: %__MODULE__{
          status: :accepted | :duplicate | :rejected | :failed,
          id: String.t() | nil,
          error: Exception.t() | nil
        }
end
