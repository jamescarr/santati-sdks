# frozen_string_literal: true

require "json"

module Santati
  # One stored event as handed out by an outbox store's `claim`.
  OutboxEntry = Struct.new(:id, :event)

  # What `post_send` is told about one event. `status` is `"accepted"`,
  # `"duplicate"`, `"rejected"` or `"failed"`; `id` is the server's event id
  # for the first two, `error` a {Santati::Error} for the last two.
  SendOutcome = Struct.new(:status, :id, :error)

  # A bounded in-memory outbox store: FIFO, thread-safe, lost on exit.
  #
  # A store is any object with `enqueue(event)`, `claim(limit)`, `ack(ids)` and
  # `release(ids)`; see `docs/sdk-surface.md`.
  class MemoryOutbox
    def initialize(max_pending: 10_000)
      unless max_pending.is_a?(Integer) && max_pending >= 1
        raise ValidationError.new("max_pending must be at least 1", field: "max_pending")
      end

      @max_pending = max_pending
      @mutex = Mutex.new
      @pending = []
      @claimed = {}
      @counter = 0
    end

    def enqueue(event)
      @mutex.synchronize do
        if @pending.length + @claimed.length >= @max_pending
          raise OutboxError.new("the outbox is full (#{@max_pending} events)", code: "outbox_full")
        end

        @counter += 1
        @pending << OutboxEntry.new(id: @counter.to_s, event: event)
      end
      nil
    end

    def claim(limit)
      @mutex.synchronize do
        entries = @pending.shift([limit, 0].max)
        entries.each { |entry| @claimed[entry.id] = entry }
        entries
      end
    end

    def ack(ids)
      @mutex.synchronize { ids.each { |id| @claimed.delete(id) } }
      nil
    end

    def release(ids)
      @mutex.synchronize do
        entries = ids.filter_map { |id| @claimed.delete(id) }
        @pending.unshift(*entries)
      end
      nil
    end
  end

  # The per-client outbox worker: `log` enqueues, a daemon thread (started by
  # the first `log`) runs a pass every `flush_interval_ms`, `flush` runs one
  # pass synchronously and `close` stops the thread and flushes.
  #
  # @api private
  class Outbox
    RETRYABLE = [TransportError, ServerError, RateLimitedError].freeze

    def initialize(client, store:, batch_size:, flush_interval_ms:, pre_send:, post_send:)
      @client = client
      @store = store
      @batch_size = batch_size
      @interval = flush_interval_ms / 1000.0
      @pre_send = pre_send
      @post_send = post_send
      @mutex = Mutex.new
      @wake = ConditionVariable.new
      @pass_mutex = Mutex.new
      @thread = nil
      @stopping = false
      @closed = false
    end

    def log(event)
      raise OutboxError.new("the client is closed", code: "closed") if @closed

      # A snapshot, so later changes to the caller's objects do not reach the stored event.
      event = Marshal.load(Marshal.dump(event))
      guard { @store.enqueue(event) }
      start_worker
      event.fetch(:idempotency_key)
    end

    def flush
      @pass_mutex.synchronize { pass }
    end

    def close
      thread = nil
      @mutex.synchronize do
        return if @closed

        @closed = true
        @stopping = true
        @wake.signal
        thread = @thread
      end
      thread&.join
      flush
    end

    private

    def start_worker
      @mutex.synchronize do
        return if @thread || @stopping

        @thread = Thread.new { run }
        @thread.report_on_exception = false
      end
    end

    def run
      loop do
        @mutex.synchronize { @wake.wait(@mutex, @interval) unless @stopping }
        break if @stopping

        begin
          flush
        rescue
          nil # the next tick retries; the worker must survive
        end
      end
    end

    # One pass: claim, run `pre_send`, send, report, repeat while batches are full.
    def pass
      loop do
        entries = guard { @store.claim(@batch_size) }
        return if entries.empty?

        released = false
        to_send = []
        entries.each do |entry|
          out = entry.event
          if @pre_send
            begin
              out = @pre_send.call(Marshal.load(Marshal.dump(entry.event)))
            rescue => e
              guard { @store.release([entry.id]) }
              released = true
              notify(entry.event, SendOutcome.new(
                status: "failed", error: OutboxError.new(e.message, code: "hook_failed")
              ))
              next
            end
          end
          if out.nil?
            guard { @store.ack([entry.id]) }
            next
          end
          to_send << [entry, out]
        end

        released = send_batch(to_send) || released unless to_send.empty?
        return if released || entries.length < @batch_size
      end
    end

    # Sends one batch; returns true when it was released for a later pass.
    def send_batch(to_send)
      ids = to_send.map { |entry, _| entry.id }
      begin
        status, result = @client.events.emit_batch_with_status(to_send.map { |_, out| out })
      rescue Error => e
        to_send.each { |entry, _| notify(entry.event, SendOutcome.new(status: "failed", error: e)) }
        if RETRYABLE.any? { |kind| e.is_a?(kind) }
          guard { @store.release(ids) }
          return true
        end
        guard { @store.ack(ids) }
        return false
      rescue => e # e.g. a malformed pre_send result: reported and dropped, never raised
        failure = OutboxError.new(e.message, code: "hook_failed")
        to_send.each { |entry, _| notify(entry.event, SendOutcome.new(status: "failed", error: failure)) }
        guard { @store.ack(ids) }
        return false
      end

      result.results.each do |item|
        entry, = to_send[item.index]
        next if entry.nil?

        notify(entry.event, outcome(item, status))
      end
      guard { @store.ack(ids) }
      false
    end

    def outcome(item, http_status)
      return SendOutcome.new(status: item.status, id: item.id) unless item.status == "rejected"

      error = item.error
      SendOutcome.new(
        status: "rejected",
        error: ValidationError.new(
          error&.message || "rejected", status: http_status, code: error&.code, field: error&.field
        )
      )
    end

    def notify(event, outcome)
      @post_send&.call(event, outcome)
    rescue
      nil
    end

    # Store failures other than the SDK's own errors become `store_unavailable`.
    def guard
      yield
    rescue Error
      raise
    rescue => e
      raise OutboxError.new(e.message, code: "store_unavailable")
    end
  end
end
