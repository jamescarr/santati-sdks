# frozen_string_literal: true

module Santati
  # The retry loop every operation runs through, per `docs/sdk-surface.md`.
  module Retry
    RETRYABLE_STATUSES = [500, 502, 503, 504].freeze
    QUOTA_EXCEEDED = "quota_exceeded"

    module_function

    # Runs the block, retrying a retryable {Santati::Error} up to
    # `max_retries` times (the client's by default; 0 sends once). The block
    # must send an identical request on every attempt.
    def call(client, max_retries: client.max_retries, &block)
      attempts = 0
      loop do
        attempts += 1
        begin
          return block.call
        rescue Error => e
          raise unless retryable?(e)
          raise if attempts > max_retries

          wait = backoff_ms(client, e, attempts)
          raise if wait == :stop

          sleep(wait / 1000.0) if wait.positive?
        end
      end
    end

    def retryable?(error)
      return true if error.is_a?(TransportError)
      return true if RETRYABLE_STATUSES.include?(error.status)

      error.status == 429 && error.code != QUOTA_EXCEEDED
    end

    # Milliseconds to wait before retry number `retry_number` (1-based), or
    # `:stop` when the server asked for longer than the caller allows.
    def backoff_ms(client, error, retry_number)
      unless error.retry_after.nil?
        wait = error.retry_after * 1000
        return (wait <= client.max_backoff_ms) ? wait : :stop
      end

      ceiling = [client.initial_backoff_ms * (2**(retry_number - 1)), client.max_backoff_ms].min
      rand(0..ceiling)
    end
  end
end
