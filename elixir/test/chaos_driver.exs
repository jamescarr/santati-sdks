# Chaos driver: emits into the outbox against the gateway in SANTATI_CHAOS and
# prints one CHAOS_RESULT line. Run by `mix run test/chaos_driver.exs` (via
# `mise run chaos elixir`, chaos/run.mjs); inert unless SANTATI_CHAOS is set.
defmodule Chaos do
  def run(cfg) do
    opts = cfg["client"]

    {:ok, base} =
      Santati.new(
        api_key: "sat_sk_chaos",
        base_url: opts["base_url"],
        trail: "chaos",
        timeout_ms: opts["timeout_ms"],
        max_retries: opts["max_retries"],
        initial_backoff_ms: opts["initial_backoff_ms"],
        max_backoff_ms: opts["max_backoff_ms"]
      )

    {:ok, pid} =
      Santati.Outbox.start_link(
        client: base,
        store: {Santati.Outbox.Memory, max_pending: opts["max_pending"]},
        batch_size: opts["batch_size"],
        flush_interval_ms: opts["flush_interval_ms"]
      )

    client = %{base | outbox: pid}
    parent = self()

    heartbeat = spawn(fn -> heartbeat(parent, cfg["heartbeat_ms"], 0.0) end)

    flusher =
      if cfg["flusher_interval_ms"] > 0 do
        spawn(fn -> flusher(parent, pid, cfg["flusher_interval_ms"], 0, 0) end)
      end

    monitors =
      for i <- 0..(cfg["emitters"] - 1) do
        spawn_monitor(fn -> send(parent, {:emitter, self(), emitter(client, i, cfg)}) end)
      end

    emitted = Enum.map(monitors, &collect/1)

    {flush_calls, flusher_exits} =
      case flusher do
        nil ->
          {0, 0}

        flusher ->
          send(flusher, :stop)
          receive do: ({:flusher, calls, exits} -> {calls, exits})
      end

    {close_ms, close_error} = close(pid)

    send(heartbeat, :stop)
    max_lag = receive do: ({:heartbeat, lag} -> lag)

    latencies = emitted |> Enum.flat_map(& &1.latencies) |> Enum.sort()

    errors =
      Enum.reduce(emitted, %{}, fn e, acc ->
        Map.merge(acc, e.errors, fn _k, a, b -> a + b end)
      end)

    errors = if close_error, do: Map.update(errors, close_error, 1, &(&1 + 1)), else: errors

    %{
      "sdk" => "elixir",
      "emits" => length(latencies),
      "queued" => Enum.sum(Enum.map(emitted, & &1.queued)),
      "errors" => errors,
      "caller_exits" => Enum.sum(Enum.map(emitted, & &1.exits)) + flusher_exits,
      "emit_ms" => %{
        "p50" => round3(percentile(latencies, 0.50)),
        "p99" => round3(percentile(latencies, 0.99)),
        "max" => round3(List.last(latencies) || 0.0)
      },
      "heartbeat_max_lag_ms" => round3(max_lag),
      "close_ms" => round3(close_ms),
      "flush_calls" => flush_calls
    }
  end

  # One emitter process: its own tally, sent back when it finishes.
  defp emitter(client, index, cfg) do
    Enum.reduce(
      0..(cfg["events_per_emitter"] - 1),
      %{latencies: [], queued: 0, errors: %{}, exits: 0},
      fn seq, acc ->
        event = %{
          event: "chaos.event",
          metadata: %{"emitter" => Integer.to_string(index), "seq" => Integer.to_string(seq)}
        }

        start = System.monotonic_time(:microsecond)

        acc =
          try do
            case Santati.Events.emit(client, event) do
              {:ok, %Santati.EmitResult{queued: true}} -> %{acc | queued: acc.queued + 1}
              {:ok, _result} -> acc
              {:error, error} -> %{acc | errors: bump(acc.errors, error_key(error))}
            end
          catch
            :exit, _reason -> %{acc | exits: acc.exits + 1}
          end

        elapsed_ms = (System.monotonic_time(:microsecond) - start) / 1000
        if cfg["emit_interval_ms"] > 0, do: Process.sleep(cfg["emit_interval_ms"])
        %{acc | latencies: [elapsed_ms | acc.latencies]}
      end
    )
  end

  defp collect({pid, ref}) do
    receive do
      {:emitter, ^pid, tally} ->
        Process.demonitor(ref, [:flush])
        tally

      {:DOWN, ^ref, :process, ^pid, _reason} ->
        %{latencies: [], queued: 0, errors: %{}, exits: 1}
    end
  end

  defp heartbeat(parent, every_ms, max_lag) do
    start = System.monotonic_time(:microsecond)
    Process.sleep(every_ms)
    lag = (System.monotonic_time(:microsecond) - start) / 1000 - every_ms
    max_lag = max(max_lag, lag)

    receive do
      :stop -> send(parent, {:heartbeat, max_lag})
    after
      0 -> heartbeat(parent, every_ms, max_lag)
    end
  end

  defp flusher(parent, outbox, every_ms, calls, exits) do
    receive do
      :stop -> send(parent, {:flusher, calls, exits})
    after
      every_ms ->
        exits =
          try do
            _ = Santati.Outbox.flush(outbox)
            exits
          catch
            :exit, _reason -> exits + 1
          end

        flusher(parent, outbox, every_ms, calls + 1, exits)
    end
  end

  defp close(pid) do
    start = System.monotonic_time(:microsecond)

    error =
      try do
        :ok = Santati.Outbox.stop(pid)
        nil
      catch
        :exit, _reason -> "exit:close"
      end

    {(System.monotonic_time(:microsecond) - start) / 1000, error}
  end

  defp error_key(%module{} = error),
    do: "#{module |> Module.split() |> List.last()}:#{error.code}"

  defp bump(counts, key), do: Map.update(counts, key, 1, &(&1 + 1))

  defp percentile([], _q), do: 0.0
  defp percentile(sorted, q), do: Enum.at(sorted, max(ceil(q * length(sorted)) - 1, 0))

  defp round3(value), do: Float.round(value * 1.0, 3)
end

case System.get_env("SANTATI_CHAOS") do
  nil -> IO.puts("SANTATI_CHAOS not set; skipping")
  raw -> IO.puts("CHAOS_RESULT " <> JSON.encode!(Chaos.run(JSON.decode!(raw))))
end
