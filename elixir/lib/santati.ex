defmodule Santati do
  @moduledoc """
  Official Elixir SDK for the Santati audit-log API.

      {:ok, client} = Santati.new(api_key: System.fetch_env!("SANTATI_API_KEY"), trail: "billing")

      {:ok, result} = Santati.Events.emit(client, %{event: "invoice.voided"})
      result.event.id

      {:ok, page} = Santati.Events.list(client, trail: "billing", limit: 50)

      client
      |> Santati.Events.stream(trail: "billing")
      |> Enum.each(&IO.inspect(&1.id))

      {:ok, _outbox} = Santati.Outbox.start_link(client: client, name: MyApp.Santati)
      {:ok, key} = Santati.log(MyApp.Santati, %{event: "invoice.voided"})
      :ok = Santati.flush(MyApp.Santati)

  `log/2` stores the event in an outbox and answers its idempotency key; a
  background worker sends it in batches (see `Santati.Outbox`).

  Every call answers `{:ok, result}` or `{:error, exception}`; `stream/2`
  raises the exception of the page that failed, and an event JSON cannot
  represent (a tuple, a pid or a reference) raises `Protocol.UndefinedError`
  from `Santati.Events.emit/2`, `emit_batch/2` and `log/2`.
  """

  alias Santati.Client

  @doc """
  Builds a client.

  Options:

    * `:api_key` — required, non-empty team API key (`sat_sk_…`)
    * `:base_url` — defaults to `https://api.santati.io`
    * `:trail` — default trail for emits; never applied to reads
    * `:timeout_ms` — per attempt, defaults to `10_000`
    * `:max_retries` — retries after the first attempt, defaults to `2`
    * `:initial_backoff_ms` — defaults to `250`
    * `:max_backoff_ms` — defaults to `8_000`
    * `:headers` — extra headers sent on every request

  Answers `{:error, %Santati.ValidationError{}}` for an empty `:api_key`
  (field `api_key`) or an `authorization` header in `:headers` (field
  `headers`).
  """
  @spec new(keyword()) :: {:ok, Client.t()} | {:error, Santati.ValidationError.t()}
  def new(options \\ []), do: Client.new(options)

  @doc "Logs an event through the `Santati.Outbox` process `server`; see `Santati.Outbox.log/2`."
  @spec log(GenServer.server(), map() | keyword()) :: {:ok, String.t()} | {:error, Exception.t()}
  defdelegate log(server, event), to: Santati.Outbox

  @doc "Runs one outbox pass synchronously; see `Santati.Outbox.flush/2`."
  @spec flush(GenServer.server(), timeout()) :: :ok | {:error, Exception.t()}
  defdelegate flush(server, timeout \\ :infinity), to: Santati.Outbox

  @doc "Returns the version of this SDK."
  @spec version() :: String.t()
  def version do
    case Application.spec(:santati, :vsn) do
      version when is_list(version) -> List.to_string(version)
      _version -> "0.0.0"
    end
  end
end
