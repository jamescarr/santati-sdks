defmodule Santati.OutboxTest do
  use ExUnit.Case, async: true

  alias Santati.SendOutcome

  test "an unexpected send failure is reported and dropped" do
    {:ok, client} = Santati.new(api_key: "sat_sk_x", base_url: "http://127.0.0.1:1")
    test = self()

    {:ok, pid} =
      Santati.Outbox.start_link(
        client: client,
        store: {Santati.Outbox.Memory, max_pending: 10},
        flush_interval_ms: 60_000,
        # A pid key has no String.Chars: emit_batch raises instead of returning an error.
        pre_send: fn _event -> %{"event" => "a.b", "trail" => "t", "data" => %{self() => 1}} end,
        post_send: fn _event, outcome -> send(test, {:outcome, outcome}) end
      )

    assert {:ok, %Santati.EmitResult{queued: true}} =
             Santati.Events.emit(%{client | outbox: pid}, %{event: "a.b", trail: "t"})

    assert :ok = Santati.Outbox.flush(pid)

    assert_receive {:outcome,
                    %SendOutcome{
                      status: :failed,
                      error: %Santati.OutboxError{code: "hook_failed"}
                    }}

    # Dropped, not released: a second pass has nothing to send.
    assert :ok = Santati.Outbox.flush(pid)
    refute_receive {:outcome, _}
    assert Process.alive?(pid)
  end

  test "an event JSON cannot encode is reported and dropped, not retried" do
    {:ok, client} = Santati.new(api_key: "sat_sk_x", base_url: "http://127.0.0.1:1")
    test = self()

    {:ok, pid} =
      Santati.Outbox.start_link(
        client: client,
        store: {Santati.Outbox.Memory, max_pending: 10},
        flush_interval_ms: 60_000,
        post_send: fn _event, outcome -> send(test, {:outcome, outcome}) end
      )

    assert {:ok, %Santati.EmitResult{queued: true}} =
             Santati.Events.emit(%{client | outbox: pid}, %{
               event: "a.b",
               trail: "t",
               data: {1, 2}
             })

    assert :ok = Santati.Outbox.flush(pid)

    assert_receive {:outcome,
                    %SendOutcome{
                      status: :failed,
                      error: %Santati.OutboxError{code: "hook_failed"}
                    }}

    assert :ok = Santati.Outbox.flush(pid)
    refute_receive {:outcome, _}
  end

  test "an event emit raises on raises in the caller, not in the process" do
    {:ok, client} = Santati.new(api_key: "sat_sk_x", base_url: "http://127.0.0.1:1")

    {:ok, pid} =
      Santati.Outbox.start_link(
        client: client,
        store: {Santati.Outbox.Memory, max_pending: 10},
        flush_interval_ms: 60_000
      )

    emitting = %{client | outbox: pid}

    assert {:ok, %Santati.EmitResult{queued: true}} =
             Santati.Events.emit(emitting, %{event: "a.b", trail: "t"})

    assert_raise Protocol.UndefinedError, fn ->
      Santati.Events.emit(emitting, %{event: "a.b", trail: "t", data: %{{:x} => 1}})
    end

    assert Process.alive?(pid)

    assert {:ok, %Santati.EmitResult{queued: true}} =
             Santati.Events.emit(emitting, %{event: "c.d", trail: "t"})
  end

  test "a queued emit is not held up by a flush against a hanging ingest" do
    {:ok, listen} = :gen_tcp.listen(0, [:binary, active: false, reuseaddr: true])
    {:ok, port} = :inet.port(listen)
    # Accepts every connection and never answers; the sockets die with it.
    acceptor = spawn(fn -> accept_forever(listen) end)

    on_exit(fn ->
      Process.exit(acceptor, :kill)
      :gen_tcp.close(listen)
    end)

    {:ok, client} =
      Santati.new(
        api_key: "sat_sk_x",
        base_url: "http://127.0.0.1:#{port}",
        timeout_ms: 2000,
        max_retries: 0
      )

    {:ok, pid} =
      Santati.Outbox.start_link(
        client: client,
        store: {Santati.Outbox.Memory, max_pending: 10},
        flush_interval_ms: 60_000
      )

    queuing = %{client | outbox: pid}

    assert {:ok, %Santati.EmitResult{queued: true}} =
             Santati.Events.emit(queuing, %{event: "a.b", trail: "t"})

    flush = Task.async(fn -> Santati.Outbox.flush(pid) end)
    Process.sleep(100)

    {micros, result} =
      :timer.tc(fn -> Santati.Events.emit(queuing, %{event: "a.b", trail: "t"}) end)

    assert {:ok, %Santati.EmitResult{queued: true}} = result
    assert micros < 500_000
    assert :ok = Task.await(flush, 10_000)
  end

  defp accept_forever(listen) do
    case :gen_tcp.accept(listen) do
      {:ok, _socket} -> accept_forever(listen)
      {:error, _closed} -> :ok
    end
  end
end
