defmodule Santati.Outbox do
  @moduledoc """
  The outbox behind `Santati.Events.emit/2` for a client built with `outbox:`.

  A `Santati.Outbox` process owns an outbox store (`Santati.Outbox.Memory` by
  default, `Santati.Outbox.Redis` for a durable one). Name it in the client's
  `:outbox` option and `Santati.Events.emit/2` validates an event exactly as a
  plain emit does, stores the resolved envelope here and answers
  `%Santati.EmitResult{queued: true}` with its idempotency key; it never makes
  a request. Every `:flush_interval_ms` the process runs a *pass*: it claims up
  to `:batch_size` entries, runs `:pre_send` on each, sends the survivors
  through `Santati.Events.emit_batch/2` (in a `Task`, so `emit/2` is never
  blocked by a slow request), reports one `:post_send` call per event and
  acknowledges what was sent. A pass sends each batch once: the client's
  `:max_retries` does not apply, and a released batch is retried by a later
  pass. A batch that fails with a `Santati.TransportError`,
  `Santati.ServerError` or `Santati.RateLimitedError` is released and the pass
  ends; any other failure drops the batch.

      children = [
        {Santati.Outbox, client: client, name: MyApp.Santati, batch_size: 100}
      ]

      {:ok, client} = Santati.new(api_key: "sat_sk_...", trail: "billing", outbox: MyApp.Santati)
      {:ok, %Santati.EmitResult{queued: true}} = Santati.Events.emit(client, %{event: "invoice.voided"})
      :ok = Santati.Outbox.flush(MyApp.Santati)

  Options of `start_link/1`:

    * `:client` — required `%Santati.Client{}`
    * `:name` — registers the process
    * `:store` — a `Santati.Outbox.Store` module or `{module, opts}`; defaults
      to `Santati.Outbox.Memory`
    * `:batch_size` — envelopes per request, `1..500`, defaults to `100`
    * `:flush_interval_ms` — the tick, greater than `0`, defaults to `1000`
    * `:pre_send` — `fun(event) :: event | nil`; `nil` drops the event, a raise
      releases it and reports a `:failed` outcome with a
      `Santati.OutboxError` of code `hook_failed`
    * `:post_send` — `fun(event, %Santati.SendOutcome{})`; always receives the
      stored event, never the `:pre_send` output; its raises are ignored

  Stopping the process (`stop/2`, or a supervisor shutdown) flushes in
  `terminate/2`; that is the equivalent of `close()` in the other SDKs. An
  `emit/2` through a client whose `:outbox` names the stopped server exits the
  caller like any dead `GenServer`.

  `flush/2` runs its pass in the same `Task`; the server keeps accepting queued
  emits while it waits.
  """

  use GenServer

  alias Santati.{Client, Errors, Events, OutboxError, SendOutcome}

  defstruct [
    :client,
    :store,
    :store_state,
    :batch_size,
    :interval,
    :pre_send,
    :post_send,
    timer: nil,
    inflight: nil,
    serving: [],
    pending: []
  ]

  @doc false
  def child_spec(opts) do
    %{
      id: Keyword.get(opts, :name, __MODULE__),
      start: {__MODULE__, :start_link, [opts]},
      shutdown: 30_000
    }
  end

  @doc """
  Starts the outbox process, linked to the caller.

  Answers `{:error, %Santati.ValidationError{}}` for a `:batch_size` outside
  `1..500` (field `batch_size`), a `:flush_interval_ms` that is not positive
  (field `flush_interval_ms`) or a store that rejects its options (for example
  `max_pending` below `1`).
  """
  @spec start_link(keyword()) :: GenServer.on_start() | {:error, Exception.t()}
  def start_link(opts) do
    {name, opts} = Keyword.pop(opts, :name)
    gen_opts = if name, do: [name: name], else: []

    with {:ok, state} <- build_state(opts) do
      GenServer.start_link(__MODULE__, state, gen_opts)
    end
  end

  @doc false
  # Stores one resolved wire envelope. `Santati.Events.emit/2` builds the
  # envelope in the caller (so a raise there never reaches this process) and
  # answers `{:error, %Santati.OutboxError{}}` when the store refuses
  # (`outbox_full`) or fails (`store_unavailable`).
  @spec enqueue(GenServer.server(), map()) :: :ok | {:error, Exception.t()}
  def enqueue(server, envelope), do: GenServer.call(server, {:enqueue, envelope})

  @doc """
  Runs one pass synchronously: answers `:ok`, or `{:error, exception}` with a
  `Santati.OutboxError` (`store_unavailable`) when the store failed. Send
  failures never fail the flush; they go through the delivery policy.
  """
  @spec flush(GenServer.server(), timeout()) :: :ok | {:error, Exception.t()}
  def flush(server, timeout \\ :infinity), do: GenServer.call(server, :flush, timeout)

  @doc "Stops the process after a final flush (`terminate/2`)."
  @spec stop(GenServer.server(), timeout()) :: :ok
  def stop(server, timeout \\ :infinity), do: GenServer.stop(server, :normal, timeout)

  defp build_state(opts) do
    client = Keyword.fetch!(opts, :client)
    batch_size = Keyword.get(opts, :batch_size, 100)
    interval = Keyword.get(opts, :flush_interval_ms, 1000)
    {store, store_opts} = store_spec(Keyword.get(opts, :store, Santati.Outbox.Memory))

    cond do
      not match?(%Client{}, client) ->
        raise ArgumentError, ":client must be a %Santati.Client{}, got: #{inspect(client)}"

      not (is_integer(batch_size) and batch_size in 1..500) ->
        {:error, Errors.validation("batch_size", "batch_size must be between 1 and 500")}

      not (is_integer(interval) and interval > 0) ->
        {:error, Errors.validation("flush_interval_ms", "flush_interval_ms must be positive")}

      true ->
        with {:ok, store_state} <- store.init(store_opts) do
          {:ok,
           %__MODULE__{
             client: client,
             store: store,
             store_state: store_state,
             batch_size: batch_size,
             interval: interval,
             pre_send: Keyword.get(opts, :pre_send),
             post_send: Keyword.get(opts, :post_send)
           }}
        end
    end
  end

  defp store_spec({module, opts}) when is_atom(module), do: {module, opts}
  defp store_spec(module) when is_atom(module), do: {module, []}

  @impl true
  def init(%__MODULE__{} = state) do
    Process.flag(:trap_exit, true)
    {:ok, state}
  end

  @impl true
  def handle_call({:enqueue, envelope}, _from, state) do
    case store(state, :enqueue, [envelope]) do
      {:ok, state} -> {:reply, :ok, arm(state)}
      {:error, error, state} -> {:reply, {:error, error}, state}
    end
  end

  # A flush starts a pass at once when the server is idle; otherwise it waits
  # for the pass in flight and then gets a fresh one of its own. The reply goes
  # out from `pass_ended/2`, so the server stays free to enqueue meanwhile.
  def handle_call(:flush, from, %{inflight: nil} = state),
    do: {:noreply, tick(%{state | serving: [from]})}

  def handle_call(:flush, from, state),
    do: {:noreply, %{state | pending: [from | state.pending]}}

  @impl true
  def handle_info(:tick, state), do: {:noreply, tick(%{state | timer: nil})}

  def handle_info(
        {ref, {:batch_done, ack_ids, release_ids, continue?}},
        %{inflight: %{ref: ref}} = state
      ) do
    Process.demonitor(ref, [:flush])
    state = %{state | inflight: nil}

    case finish(state, ack_ids, release_ids) do
      {:ok, state} -> {:noreply, if(continue?, do: tick(state), else: pass_ended(state, :ok))}
      {:error, error, state} -> {:noreply, pass_ended(state, {:error, error})}
    end
  end

  def handle_info(
        {:DOWN, ref, :process, _pid, _reason},
        %{inflight: %{ref: ref} = flight} = state
      ) do
    state = %{state | inflight: nil}

    case finish(state, [], flight.ids) do
      {:ok, state} -> {:noreply, pass_ended(state, :ok)}
      {:error, error, state} -> {:noreply, pass_ended(state, {:error, error})}
    end
  end

  def handle_info(_message, state), do: {:noreply, state}

  @impl true
  def terminate(_reason, state) do
    _ = settle(state)
    :ok
  rescue
    _error -> :ok
  end

  # One tick: claim a batch and send it in a task. Store failures end the pass;
  # only a flush caller is told (`serving`), the next tick retries.
  defp tick(%{inflight: nil} = state) do
    case launch(state) do
      {:sent, state} -> state
      {:empty, state} -> pass_ended(state, :ok)
      {:error, error, state} -> pass_ended(state, {:error, error})
    end
  end

  defp tick(state), do: state

  defp launch(state) do
    case store(state, :claim, [state.batch_size]) do
      {:ok, [], state} ->
        {:empty, state}

      {:ok, entries, state} ->
        task = Task.async(fn -> send_batch(state, entries) end)
        {:sent, %{state | inflight: %{ref: task.ref, ids: Enum.map(entries, & &1.id)}}}

      {:error, error, state} ->
        {:error, error, state}
    end
  end

  # A pass is over: answer the flush callers it served, then start the fresh
  # pass the callers who arrived meanwhile are owed, or re-arm the timer.
  defp pass_ended(state, reply) do
    Enum.each(state.serving, &GenServer.reply(&1, reply))

    case state.pending do
      [] -> arm(%{state | serving: []})
      pending -> tick(%{state | serving: pending, pending: []})
    end
  end

  # Waits for a batch in flight, then runs passes inline until one ends.
  defp settle(state) do
    with {:ok, state} <- await(state), do: pass(state)
  end

  defp await(%{inflight: nil} = state), do: {:ok, state}

  defp await(%{inflight: %{ref: ref} = flight} = state) do
    state = %{state | inflight: nil}

    receive do
      {^ref, {:batch_done, ack_ids, release_ids, _continue?}} ->
        Process.demonitor(ref, [:flush])
        finish(state, ack_ids, release_ids)

      {:DOWN, ^ref, :process, _pid, _reason} ->
        finish(state, [], flight.ids)
    end
  end

  defp pass(state) do
    case store(state, :claim, [state.batch_size]) do
      {:ok, [], state} ->
        {:ok, state}

      {:ok, entries, state} ->
        {:batch_done, ack_ids, release_ids, continue?} = send_batch(state, entries)

        case finish(state, ack_ids, release_ids) do
          {:ok, state} -> if continue?, do: pass(state), else: {:ok, state}
          {:error, error, state} -> {:error, error, state}
        end

      {:error, error, state} ->
        {:error, error, state}
    end
  end

  defp finish(state, ack_ids, release_ids) do
    with {:ok, state} <- store_ids(state, :ack, ack_ids),
         do: store_ids(state, :release, release_ids)
  end

  defp store_ids(state, _fun, []), do: {:ok, state}
  defp store_ids(state, fun, ids), do: store(state, fun, [ids])

  defp arm(%{timer: nil, inflight: nil} = state) do
    %{state | timer: Process.send_after(self(), :tick, state.interval)}
  end

  defp arm(state), do: state

  defp store(state, fun, args) do
    result =
      try do
        apply(state.store, fun, [state.store_state | args])
      rescue
        error -> {:error, error}
      end

    case result do
      {:ok, store_state} -> {:ok, %{state | store_state: store_state}}
      {:ok, entries, store_state} -> {:ok, entries, %{state | store_state: store_state}}
      {:error, error} -> {:error, store_error(error), state}
      other -> {:error, store_error({:unexpected_reply, other}), state}
    end
  end

  defp store_error(%OutboxError{} = error), do: error

  defp store_error(error) when is_exception(error),
    do: Errors.outbox("store_unavailable", Exception.message(error))

  defp store_error(error), do: Errors.outbox("store_unavailable", inspect(error))

  # Runs the hooks and one `emit_batch`; answers what to ack and what to
  # release. Pure of the server's state, so it runs in a task or inline.
  defp send_batch(state, entries) do
    {to_send, ack_ids, released} =
      Enum.reduce(entries, {[], [], MapSet.new()}, fn entry, {to_send, ack_ids, released} ->
        case run_pre_send(state.pre_send, entry.event) do
          {:ok, nil} ->
            {to_send, [entry.id | ack_ids], released}

          {:ok, out} ->
            {[{entry, out} | to_send], ack_ids, released}

          {:error, error} ->
            error = Errors.outbox("hook_failed", error)
            notify(state.post_send, entry.event, %SendOutcome{status: :failed, error: error})
            {to_send, ack_ids, MapSet.put(released, entry.id)}
        end
      end)

    to_send = Enum.reverse(to_send)
    {sent_ack, released} = send_events(state, to_send, released)
    ack_ids = sent_ack ++ ack_ids
    release_ids = for entry <- entries, MapSet.member?(released, entry.id), do: entry.id

    {:batch_done, ack_ids, release_ids, release_ids == [] and length(entries) >= state.batch_size}
  end

  defp send_events(_state, [], released), do: {[], released}

  defp send_events(state, to_send, released) do
    ids = Enum.map(to_send, fn {entry, _out} -> entry.id end)

    case emit(state.client, Enum.map(to_send, fn {_entry, out} -> out end)) do
      {:ok, result, status} ->
        sent = List.to_tuple(to_send)

        Enum.each(result.results, fn item ->
          if is_integer(item.index) and item.index >= 0 and item.index < tuple_size(sent) do
            {entry, _out} = elem(sent, item.index)
            notify(state.post_send, entry.event, outcome(item, status))
          end
        end)

        {ids, released}

      {:error, error} ->
        Enum.each(to_send, fn {entry, _out} ->
          notify(state.post_send, entry.event, %SendOutcome{status: :failed, error: error})
        end)

        if retryable?(error), do: {[], Enum.into(ids, released)}, else: {ids, released}
    end
  end

  # E.g. a malformed :pre_send result: a failed, non-retryable outcome instead of a crashed Task.
  defp emit(client, events) do
    Events.emit_batch_with_status(%{client | max_retries: 0}, events)
  rescue
    error -> {:error, Errors.outbox("hook_failed", Exception.message(error))}
  catch
    kind, reason -> {:error, Errors.outbox("hook_failed", Exception.format_banner(kind, reason))}
  end

  defp retryable?(%Santati.TransportError{}), do: true
  defp retryable?(%Santati.ServerError{}), do: true
  defp retryable?(%Santati.RateLimitedError{}), do: true
  defp retryable?(_error), do: false

  defp outcome(%{status: "rejected", error: error}, status) do
    error = error || %Santati.BatchItemError{}

    %SendOutcome{
      status: :rejected,
      error:
        struct(Errors.validation_kind(error.code), %{
          status: status,
          code: error.code,
          field: error.field,
          message: error.message || "event rejected"
        })
    }
  end

  defp outcome(%{status: "duplicate", id: id}, _status),
    do: %SendOutcome{status: :duplicate, id: id}

  defp outcome(%{id: id}, _status), do: %SendOutcome{status: :accepted, id: id}

  defp run_pre_send(nil, event), do: {:ok, event}

  defp run_pre_send(hook, event) do
    {:ok, hook.(event)}
  rescue
    error -> {:error, Exception.message(error)}
  catch
    kind, reason -> {:error, Exception.format_banner(kind, reason)}
  end

  defp notify(nil, _event, _outcome), do: :ok

  defp notify(hook, event, outcome) do
    hook.(event, outcome)
    :ok
  rescue
    _error -> :ok
  catch
    _kind, _reason -> :ok
  end
end
