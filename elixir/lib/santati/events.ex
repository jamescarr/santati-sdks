defmodule Santati.Events do
  @moduledoc """
  Emitting and reading audit events.

  All functions take a `Santati.Client`. Reads never apply the client's default
  trail; an emit resolves the trail from the event first, then from the client.
  """

  alias Santati.{BatchItem, BatchItemError, BatchResult, Client, EmitResult, Errors, EventPage}
  alias SantatiCore.Deserializer
  alias SantatiCore.Model.AuditEvent
  alias SantatiCore.Model.ErrorBody
  alias SantatiCore.Model.EventBatchItemResult
  alias SantatiCore.Model.EventBatchResult
  alias SantatiCore.Model.PaginatedAuditEventList

  @events_path "/api/v0/events/"

  @list_parameters [
    :actor_id,
    :actor_type,
    :created_after,
    :created_before,
    :cursor,
    :event,
    :event_prefix,
    :limit,
    :organization_id,
    :q,
    :sort,
    :target_id,
    :target_type,
    :trail
  ]

  @typedoc "Read parameters: a keyword list or a map, string or atom keys."
  @type params :: keyword() | map()

  @doc """
  Emits one audit event.

  The event is a map (atom or string keys) with `event` required and `trail`,
  `organization_id`, `actor`, `targets`, `metadata`, `data`, `context`,
  `created_at` and `idempotency_key` optional. Absent and `nil` members are
  never sent. A missing `idempotency_key` gets a freshly generated UUIDv4.

  Answers `{:ok, %Santati.EmitResult{}}` with `duplicate: true` when the server
  replayed an earlier request, `{:error, exception}` otherwise.
  """
  @spec emit(Client.t(), map() | keyword()) :: {:ok, EmitResult.t()} | {:error, Exception.t()}
  def emit(%Client{} = client, event) do
    with {:ok, envelope, key} <- envelope(client, event, "") do
      case Client.request(client, :post, @events_path, body: envelope) do
        {:ok, %{status: status, body: body}} when status in [200, 201] ->
          case decode(body, AuditEvent) do
            {:ok, %AuditEvent{} = stored} ->
              {:ok, %EmitResult{event: stored, duplicate: status == 200, idempotency_key: key}}

            _other ->
              {:error, Errors.api(status)}
          end

        {:ok, %{status: status}} ->
          {:error, Errors.api(status)}

        {:error, error} ->
          {:error, error}
      end
    end
  end

  @doc """
  Emits a batch of audit events in one request.

  Every item follows the `emit/2` rules; the first invalid item wins and names
  itself in the error's `field` (`events[<i>].event`, `events[<i>].trail`).
  Each item lacking an `idempotency_key` gets its own generated UUIDv4.

  Answers `{:ok, %Santati.BatchResult{}}` for `202` and `207` (a partial
  rejection is a result, not an error).
  """
  @spec emit_batch(Client.t(), [map() | keyword()]) ::
          {:ok, BatchResult.t()} | {:error, Exception.t()}
  def emit_batch(%Client{} = client, events) when is_list(events) do
    if events == [] do
      {:error, Errors.validation("events", "events must not be empty")}
    else
      with {:ok, envelopes} <- envelopes(client, events) do
        case Client.request(client, :post, @events_path, body: %{"events" => envelopes}) do
          {:ok, %{status: status, body: body}} when status in [202, 207] ->
            case decode(body, EventBatchResult) do
              {:ok, %EventBatchResult{} = result} ->
                {:ok, batch_result(result)}

              _other ->
                {:error, Errors.api(status)}
            end

          {:ok, %{status: status}} ->
            {:error, Errors.api(status)}

          {:error, error} ->
            {:error, error}
        end
      end
    end
  end

  @doc """
  Lists audit events, newest first.

  Only the supplied parameters are sent, in spec (alphabetical) order:

    * `:trail`, `:event`, `:event_prefix`, `:organization_id`
    * `:actor_id`, `:actor_type`, `:target_type`, `:target_id`
    * `:created_after`, `:created_before`, `:q`, `:sort`
    * `:limit`, `:cursor`

  Answers `{:ok, %Santati.EventPage{}}`; `next_cursor` is the cursor decoded
  from the response's `next` URL, `nil` on the last page.
  """
  @spec list(Client.t(), params()) :: {:ok, EventPage.t()} | {:error, Exception.t()}
  def list(%Client{} = client, params \\ []) do
    with {:ok, query} <- query(params) do
      case Client.request(client, :get, @events_path, query: query) do
        {:ok, %{status: 200, body: body}} ->
          case decode(body, PaginatedAuditEventList) do
            {:ok, %PaginatedAuditEventList{} = page} ->
              {:ok, %EventPage{results: page.results || [], next_cursor: next_cursor(page.next)}}

            _other ->
              {:error, Errors.api(200)}
          end

        {:ok, %{status: status}} ->
          {:error, Errors.api(status)}

        {:error, error} ->
          {:error, error}
      end
    end
  end

  @doc """
  Lazily iterates every audit event matching `params` (the same parameters as
  `list/2`, minus `:cursor`), following `next` cursors page by page.

  Returns an `Enumerable` of `SantatiCore.Model.AuditEvent`. An error on a
  later page is raised after the earlier events were yielded.
  """
  @spec stream(Client.t(), params()) :: Enumerable.t()
  def stream(%Client{} = client, params \\ []) do
    case to_keyword(params) do
      {:ok, keyword} ->
        Stream.resource(
          fn -> nil end,
          fn
            :halt -> {:halt, :halt}
            cursor -> next_page(client, keyword, cursor)
          end,
          fn _state -> :ok end
        )

      :error ->
        raise Errors.validation(nil, "params must be a keyword list or a map")
    end
  end

  defp next_page(client, params, cursor) do
    case list(client, cursor_param(params, cursor)) do
      {:ok, %EventPage{results: results, next_cursor: next}} ->
        {results, if(is_binary(next) and next != "", do: next, else: :halt)}

      {:error, error} ->
        raise error
    end
  end

  defp cursor_param(params, nil), do: params
  defp cursor_param(params, cursor), do: Keyword.put(params, :cursor, cursor)

  defp envelope(client, event, prefix) do
    event = event_map(event)
    name = Map.get(event, "event")
    trail = resolve_trail(event, client)

    cond do
      not is_binary(name) or name == "" ->
        {:error, Errors.validation(prefix <> "event", "event must be a non-empty string")}

      not is_binary(trail) or trail == "" ->
        {:error, Errors.validation(prefix <> "trail", "trail must be a non-empty string")}

      true ->
        key = key_or_new(Map.get(event, "idempotency_key"))

        members = %{
          "event" => name,
          "trail" => trail,
          "created_at" => Map.get(event, "created_at"),
          "organization_id" => Map.get(event, "organization_id"),
          "idempotency_key" => key,
          "actor" => member(Map.get(event, "actor")),
          "targets" => targets(Map.get(event, "targets")),
          "metadata" => Map.get(event, "metadata"),
          "data" => Map.get(event, "data"),
          "context" => Map.get(event, "context")
        }

        {:ok, drop_nils(members), key}
    end
  end

  defp envelopes(client, events) do
    events
    |> Enum.with_index()
    |> Enum.reduce_while({:ok, []}, fn {event, index}, {:ok, envelopes} ->
      case envelope(client, event, "events[#{index}].") do
        {:ok, envelope, _key} -> {:cont, {:ok, [envelope | envelopes]}}
        {:error, error} -> {:halt, {:error, error}}
      end
    end)
    |> case do
      {:ok, envelopes} -> {:ok, Enum.reverse(envelopes)}
      {:error, error} -> {:error, error}
    end
  end

  defp resolve_trail(event, client) do
    case Map.get(event, "trail") do
      trail when is_binary(trail) and trail != "" -> trail
      _trail -> client.trail
    end
  end

  # An actor and a target are the same four members on the wire.
  defp member(nil), do: nil

  defp member(value) when is_map(value) do
    value |> normalize() |> Map.take(["type", "id", "name", "metadata"]) |> drop_nils()
  end

  defp member(value), do: normalize(value)

  defp targets(nil), do: nil
  defp targets(values) when is_list(values), do: Enum.map(values, &member/1)
  defp targets(value), do: normalize(value)

  defp batch_result(%EventBatchResult{} = result) do
    %BatchResult{
      accepted: result.accepted,
      rejected: result.rejected,
      results: Enum.map(result.results || [], &batch_item/1)
    }
  end

  defp batch_item(%EventBatchItemResult{} = item) do
    %BatchItem{
      index: item.index,
      status: item.status,
      id: item.id,
      error: batch_item_error(item.error)
    }
  end

  defp batch_item_error(nil), do: nil

  defp batch_item_error(%ErrorBody{} = error) do
    %BatchItemError{code: error.code, message: error.message, field: error.field}
  end

  defp query(params) do
    case to_keyword(params) do
      {:ok, keyword} ->
        case Enum.find(keyword, fn {key, _value} -> key not in @list_parameters end) do
          {key, _value} ->
            {:error, Errors.validation(to_string(key), "unknown list parameter")}

          nil ->
            {:ok,
             keyword
             |> Enum.reject(fn {_key, value} -> is_nil(value) end)
             |> Enum.sort_by(fn {key, _value} -> to_string(key) end)}
        end

      :error ->
        {:error, Errors.validation(nil, "params must be a keyword list or a map")}
    end
  end

  defp to_keyword(params) when is_list(params), do: {:ok, normalize_keys(params)}

  defp to_keyword(params) when is_map(params),
    do: {:ok, params |> Map.to_list() |> normalize_keys()}

  defp to_keyword(_params), do: :error

  defp normalize_keys(pairs) do
    Enum.map(pairs, fn {key, value} -> {normalize_key(key), value} end)
  end

  defp normalize_key(key) when is_atom(key), do: key

  defp normalize_key(key) when is_binary(key) do
    Enum.find(@list_parameters, key, &(Atom.to_string(&1) == key))
  end

  defp normalize_key(key), do: key

  defp next_cursor(nil), do: nil

  defp next_cursor(url) when is_binary(url) do
    case URI.parse(url).query do
      nil -> nil
      query -> query |> decode_query() |> Map.get("cursor")
    end
  end

  defp next_cursor(_url), do: nil

  defp decode_query(query) do
    URI.decode_query(query)
  rescue
    _error -> %{}
  end

  defp decode(body, module), do: Deserializer.json_decode(body, module)

  defp key_or_new(key) when is_binary(key) and key != "", do: key
  defp key_or_new(_key), do: generate_key()

  defp generate_key do
    <<a::32, b::16, c::16, d::16, e::48>> = :crypto.strong_rand_bytes(16)

    [
      hex(a, 8),
      hex(b, 4),
      hex(Bitwise.bor(Bitwise.band(c, 0x0FFF), 0x4000), 4),
      hex(Bitwise.bor(Bitwise.band(d, 0x3FFF), 0x8000), 4),
      hex(e, 12)
    ]
    |> Enum.join("-")
  end

  defp hex(value, width) do
    value
    |> Integer.to_string(16)
    |> String.pad_leading(width, "0")
    |> String.downcase()
  end

  defp drop_nils(map), do: Map.reject(map, fn {_key, value} -> is_nil(value) end)

  defp event_map(event) do
    case normalize(event) do
      map when is_map(map) -> map
      _other -> %{}
    end
  end

  defp normalize(map) when is_map(map) and not is_struct(map) do
    Map.new(map, fn {key, value} -> {key_name(key), normalize(value)} end)
  end

  defp normalize([]), do: []

  defp normalize(list) when is_list(list) do
    if Keyword.keyword?(list) do
      normalize(Map.new(list))
    else
      Enum.map(list, &normalize/1)
    end
  end

  defp normalize(value), do: value

  defp key_name(key) when is_binary(key), do: key
  defp key_name(key) when is_atom(key), do: Atom.to_string(key)
  defp key_name(key), do: to_string(key)
end
