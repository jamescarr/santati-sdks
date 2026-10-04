# frozen_string_literal: true

require_relative "test_helper"
require "securerandom"

# Integration test for the Redis outbox adapter; runs when SANTATI_TEST_REDIS_URL
# points at a Redis 7 server.
class OutboxRedisTest < Minitest::Test
  def test_claim_ack_and_redelivery
    url = ENV["SANTATI_TEST_REDIS_URL"]
    skip "SANTATI_TEST_REDIS_URL not set" unless url

    require "santati/outbox/redis"
    redis = Redis.new(url: url)
    key = "santati:test:#{SecureRandom.uuid}"
    begin
      store = Santati::RedisOutbox.new(redis, key: key)
      3.times { |i| store.enqueue({event: "e#{i + 1}", trail: "t", idempotency_key: "k#{i + 1}"}) }

      first = store.claim(2)
      assert_equal %w[e1 e2], first.map { |entry| entry.event[:event] }
      assert_equal 2, first.map(&:id).uniq.length
      assert(first.none? { |entry| entry.id.empty? })

      rest = store.claim(5)
      assert_equal %w[e3], rest.map { |entry| entry.event[:event] }
      assert_empty store.claim(5)

      store.ack(first.map(&:id))
      stale = Santati::RedisOutbox.new(redis, key: key, visibility_ms: 0)
      assert_equal %w[e3], stale.claim(10).map { |entry| entry.event[:event] }

      store.ack(rest.map(&:id))
      assert_empty store.claim(5)
      assert_empty stale.claim(5)
      assert_equal 0, redis.xlen(key)
    ensure
      redis.del(key)
      redis.close
    end
  end
end
