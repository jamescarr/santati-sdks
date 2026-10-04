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

    {:ok, _key} = Santati.Outbox.log(pid, %{event: "a.b", trail: "t"})
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
end
