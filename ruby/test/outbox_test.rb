# frozen_string_literal: true

require_relative "test_helper"

# The outbox behind a queued Santati::Events#emit, without a server.
class OutboxTest < Minitest::Test
  def client(store, **options)
    Santati::Client.new(
      api_key: "sat_sk_x", base_url: "http://127.0.0.1:1", outbox: store, flush_interval_ms: 60_000, **options
    )
  end

  def test_unexpected_send_failure_is_reported_and_dropped
    store = Santati::MemoryOutbox.new
    outcomes = []
    # A Hash that emit_batch cannot read: a RuntimeError, not a Santati::Error.
    malformed = Class.new(Hash) { def transform_keys(*, &) = raise("boom") }.new
    santati = client(store, pre_send: ->(_event) { malformed }, post_send: ->(_event, outcome) { outcomes << outcome })

    santati.events.emit(event: "a.b", trail: "t")
    santati.flush

    assert_equal 1, outcomes.length
    assert_equal "failed", outcomes.first.status
    assert_instance_of Santati::OutboxError, outcomes.first.error
    assert_equal "hook_failed", outcomes.first.error.code
    assert_empty store.claim(10)
    santati.close
  end

  def test_queued_emit_snapshots_the_event
    store = Santati::MemoryOutbox.new
    santati = client(store)
    meta = {"a" => "1"}

    santati.events.emit(event: "a.b", trail: "t", metadata: meta)
    meta["a"] = "2"

    assert_equal({"a" => "1"}, store.claim(1).first.event[:metadata])
    santati.close
  end
end
