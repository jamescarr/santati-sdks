defmodule Santati.Outbox.RedisTest do
  use ExUnit.Case, async: false

  @moduletag :redis

  alias Santati.Outbox.Redis, as: Store

  defp event(i), do: %{"event" => "e#{i}", "trail" => "t", "idempotency_key" => "k#{i}"}

  test "claims in order, acks, and re-delivers stale entries" do
    {:ok, conn} = Redix.start_link(System.fetch_env!("SANTATI_TEST_REDIS_URL"))
    key = "santati:test:#{System.unique_integer([:positive])}-#{:erlang.phash2(make_ref())}"
    on_exit(fn -> cleanup(key) end)

    {:ok, store} = Store.init(conn: conn, key: key)

    store =
      Enum.reduce(1..3, store, fn i, st ->
        {:ok, st} = Store.enqueue(st, event(i))
        st
      end)

    {:ok, [first, second], store} = Store.claim(store, 2)
    assert [first.event, second.event] == [event(1), event(2)]
    assert first.id != "" and first.id != second.id

    {:ok, [third], store} = Store.claim(store, 5)
    assert third.event == event(3)
    assert {:ok, [], store} = Store.claim(store, 5)

    {:ok, store} = Store.ack(store, [first.id, second.id])

    {:ok, other} = Store.init(conn: conn, key: key, visibility_ms: 0)
    assert {:ok, [stale], other} = Store.claim(other, 10)
    assert stale.event == event(3)

    {:ok, store} = Store.ack(store, [third.id])
    assert {:ok, [], _store} = Store.claim(store, 5)
    assert {:ok, [], _other} = Store.claim(other, 5)
    assert {:ok, 0} = Redix.command(conn, ["XLEN", key])
  end

  defp cleanup(key) do
    {:ok, conn} = Redix.start_link(System.fetch_env!("SANTATI_TEST_REDIS_URL"))
    Redix.command(conn, ["DEL", key])
  end
end
