defmodule Santati.Outbox.Memory do
  @moduledoc """
  The default, bounded in-memory outbox store.

  A FIFO queue plus a claimed map. `enqueue/2` answers
  `%Santati.OutboxError{code: "outbox_full"}` once pending plus claimed entries
  reach `:max_pending` (default `10_000`, at least `1`). Entry ids are decimal
  strings of a counter starting at `"1"`. `release/2` puts entries back at the
  front of the queue in their original order.
  """

  @behaviour Santati.Outbox.Store

  alias Santati.{Errors, OutboxEntry}

  @impl true
  def init(opts) do
    max_pending = Keyword.get(opts, :max_pending, 10_000)

    if is_integer(max_pending) and max_pending >= 1 do
      {:ok, %{pending: :queue.new(), claimed: %{}, next_id: 1, max_pending: max_pending}}
    else
      {:error, Errors.validation("max_pending", "max_pending must be at least 1")}
    end
  end

  @impl true
  def enqueue(state, event) do
    if :queue.len(state.pending) + map_size(state.claimed) >= state.max_pending do
      {:error, Errors.outbox("outbox_full", "outbox is full (#{state.max_pending} pending)")}
    else
      entry = %OutboxEntry{id: Integer.to_string(state.next_id), event: event}

      {:ok, %{state | pending: :queue.in(entry, state.pending), next_id: state.next_id + 1}}
    end
  end

  @impl true
  def claim(state, n) do
    {taken, rest} = :queue.split(min(n, :queue.len(state.pending)), state.pending)
    entries = :queue.to_list(taken)
    claimed = Enum.reduce(entries, state.claimed, &Map.put(&2, &1.id, &1))
    {:ok, entries, %{state | pending: rest, claimed: claimed}}
  end

  @impl true
  def ack(state, ids), do: {:ok, %{state | claimed: Map.drop(state.claimed, ids)}}

  @impl true
  def release(state, ids) do
    entries = for id <- ids, entry = Map.get(state.claimed, id), do: entry

    {:ok,
     %{
       state
       | pending: :queue.join(:queue.from_list(entries), state.pending),
         claimed: Map.drop(state.claimed, ids)
     }}
  end
end
