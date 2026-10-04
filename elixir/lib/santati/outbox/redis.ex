if Code.ensure_loaded?(Redix) do
  defmodule Santati.Outbox.Redis do
    @moduledoc """
    A durable outbox store on Redis Streams, over your own `Redix` connection.

        {:ok, conn} = Redix.start_link("redis://localhost:6379")

        {Santati.Outbox,
         client: client, store: {Santati.Outbox.Redis, conn: conn, key: "santati:outbox"}}

    Options: `:conn` (required Redix connection pid or name), `:key` (the
    stream, defaults to `"santati:outbox"`) and `:visibility_ms` (how long a
    claimed entry stays invisible before another claim re-delivers it,
    defaults to `60_000`).

    The layout is shared by every Santati SDK, so any of them can drain what
    another enqueued: consumer group `santati`, one `event` field holding the
    compact JSON of the wire envelope, `XAUTOCLAIM` for stale entries and
    `XREADGROUP` for new ones. `release/2` is a no-op: released entries stay in
    the pending list and are re-delivered after `:visibility_ms`. The store has
    no capacity bound. Add `{:redix, "~> 1.5"}` to your dependencies to use it.
    """

    @behaviour Santati.Outbox.Store

    alias Santati.{Errors, OutboxEntry}

    @group "santati"

    @impl true
    def init(opts) do
      case Keyword.fetch(opts, :conn) do
        {:ok, conn} ->
          {:ok,
           %{
             conn: conn,
             key: Keyword.get(opts, :key, "santati:outbox"),
             visibility_ms: Keyword.get(opts, :visibility_ms, 60_000),
             consumer: uuid(),
             ready: false
           }}

        :error ->
          {:error, Errors.validation("conn", "conn is required")}
      end
    end

    @impl true
    def enqueue(state, event) do
      with {:ok, state} <- ensure_group(state),
           {:ok, _id} <- command(state, ["XADD", state.key, "*", "event", JSON.encode!(event)]) do
        {:ok, state}
      end
    end

    @impl true
    def claim(state, n) when n <= 0, do: {:ok, [], state}

    def claim(state, n) do
      with {:ok, state} <- ensure_group(state),
           {:ok, reply} <-
             command(state, [
               "XAUTOCLAIM",
               state.key,
               @group,
               state.consumer,
               to_string(state.visibility_ms),
               "0-0",
               "COUNT",
               to_string(n)
             ]),
           claimed = entries(state, autoclaimed(reply)),
           {:ok, fresh} <- read_new(state, n - length(claimed)) do
        {:ok, claimed ++ fresh, state}
      end
    end

    @impl true
    def ack(state, []), do: {:ok, state}

    def ack(state, ids) do
      with {:ok, _acked} <- command(state, ["XACK", state.key, @group | ids]),
           {:ok, _deleted} <- command(state, ["XDEL", state.key | ids]) do
        {:ok, state}
      end
    end

    @impl true
    def release(state, _ids), do: {:ok, state}

    defp read_new(_state, remaining) when remaining <= 0, do: {:ok, []}

    defp read_new(state, remaining) do
      with {:ok, reply} <-
             command(state, [
               "XREADGROUP",
               "GROUP",
               @group,
               state.consumer,
               "COUNT",
               to_string(remaining),
               "STREAMS",
               state.key,
               ">"
             ]) do
        {:ok, entries(state, streamed(reply))}
      end
    end

    # [next_id, [[id, [field, value, ...]], ...], deleted_ids]
    defp autoclaimed([_next, raw | _rest]) when is_list(raw), do: raw
    defp autoclaimed(_reply), do: []

    # [[key, [[id, [field, value, ...]], ...]]] or nil
    defp streamed([[_key, raw] | _rest]) when is_list(raw), do: raw
    defp streamed(_reply), do: []

    # Poison entries (no event, undecodable JSON) are removed and skipped;
    # nil or field-less placeholders of deleted entries are skipped.
    defp entries(state, raw) do
      Enum.flat_map(raw, fn
        [id, fields] when is_binary(id) and is_list(fields) ->
          case decode(fields) do
            {:ok, event} ->
              [%OutboxEntry{id: id, event: event}]

            :error ->
              _ = command(state, ["XACK", state.key, @group, id])
              _ = command(state, ["XDEL", state.key, id])
              []
          end

        _placeholder ->
          []
      end)
    end

    defp decode(fields) do
      with {:ok, json} <- field(fields, "event"),
           {:ok, %{} = event} <- JSON.decode(json) do
        {:ok, event}
      else
        _error -> :error
      end
    end

    defp field([name, value | _rest], name) when is_binary(value), do: {:ok, value}
    defp field([_name, _value | rest], name), do: field(rest, name)
    defp field(_fields, _name), do: :error

    defp ensure_group(%{ready: true} = state), do: {:ok, state}

    defp ensure_group(state) do
      case Redix.command(state.conn, ["XGROUP", "CREATE", state.key, @group, "0", "MKSTREAM"]) do
        {:ok, _ok} ->
          {:ok, %{state | ready: true}}

        {:error, %Redix.Error{message: "BUSYGROUP" <> _rest}} ->
          {:ok, %{state | ready: true}}

        {:error, error} ->
          {:error, unavailable(error)}
      end
    end

    defp command(state, command) do
      case Redix.command(state.conn, command) do
        {:ok, reply} -> {:ok, reply}
        {:error, error} -> {:error, unavailable(error)}
      end
    end

    defp unavailable(error), do: Errors.outbox("store_unavailable", Exception.message(error))

    defp uuid do
      <<a::32, b::16, c::16, d::16, e::48>> = :crypto.strong_rand_bytes(16)
      c = Bitwise.bor(Bitwise.band(c, 0x0FFF), 0x4000)
      d = Bitwise.bor(Bitwise.band(d, 0x3FFF), 0x8000)

      [a, b, c, d, e]
      |> Enum.zip([8, 4, 4, 4, 12])
      |> Enum.map_join("-", fn {value, width} ->
        value |> Integer.to_string(16) |> String.pad_leading(width, "0") |> String.downcase()
      end)
    end
  end
end
