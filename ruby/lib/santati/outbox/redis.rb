# frozen_string_literal: true

require "json"
require "redis"
require "securerandom"
require "santati"

module Santati
  # An outbox store on a Redis Stream with one consumer group (`santati`), so
  # several processes, and the other Santati SDKs, can share one outbox.
  # Requires the `redis` gem (>= 5), which the gem does not depend on.
  #
  #   require "santati/outbox/redis"
  #   outbox = Santati::RedisOutbox.new(Redis.new(url: ENV.fetch("REDIS_URL")))
  #   client = Santati::Client.new(api_key: "sat_sk_…", trail: "billing", outbox: outbox)
  class RedisOutbox
    GROUP = "santati"

    def initialize(redis, key: "santati:outbox", visibility_ms: 60_000)
      @redis = redis
      @key = key
      @visibility_ms = visibility_ms
      @consumer = SecureRandom.uuid
      @ready = false
      @mutex = Mutex.new
    end

    def enqueue(event)
      guard do
        ensure_group
        @redis.xadd(@key, {"event" => JSON.generate(event)})
      end
      nil
    end

    def claim(limit)
      return [] if limit <= 0

      guard do
        ensure_group
        raw = reclaim(limit)
        raw += read_new(limit - raw.length) if raw.length < limit
        raw.filter_map { |id, fields| entry_for(id, fields) }
      end
    end

    def ack(ids)
      return nil if ids.empty?

      guard do
        @redis.xack(@key, GROUP, *ids)
        @redis.xdel(@key, *ids)
      end
      nil
    end

    # Entries stay pending; `claim` re-delivers them after `visibility_ms`.
    def release(_ids)
      nil
    end

    private

    def ensure_group
      return if @ready

      @mutex.synchronize do
        next if @ready

        begin
          @redis.xgroup(:create, @key, GROUP, "0", mkstream: true)
        rescue ::Redis::CommandError => e
          raise unless e.message.include?("BUSYGROUP")
        end
        @ready = true
      end
    end

    def reclaim(limit)
      reply = @redis.xautoclaim(@key, GROUP, @consumer, @visibility_ms, "0-0", count: limit)
      Array(reply.is_a?(Hash) ? reply["entries"] : reply[1])
    end

    def read_new(count)
      reply = @redis.xreadgroup(GROUP, @consumer, @key, ">", count: count)
      Array(reply[@key])
    end

    # The decoded entry, or `nil` (after deleting it) when it is poison.
    def entry_for(id, fields)
      return nil if id.nil?

      json = fields.is_a?(Hash) ? fields["event"] : nil
      event = begin
        json && JSON.parse(json, symbolize_names: true)
      rescue JSON::ParserError
        nil
      end
      return poison!(id) unless event.is_a?(Hash)

      OutboxEntry.new(id: id, event: event)
    end

    def poison!(id)
      @redis.xack(@key, GROUP, id)
      @redis.xdel(@key, id)
      nil
    end

    def guard
      yield
    rescue Error
      raise
    rescue ::Redis::BaseError, SystemCallError, IOError => e
      raise OutboxError.new(e.message, code: "store_unavailable")
    end
  end
end
