# frozen_string_literal: true

require "uri"

module Santati
  # The entry point: holds the connection settings and exposes the events
  # resource.
  #
  #   client = Santati::Client.new(api_key: "sat_sk_…", trail: "billing")
  #   client.events.emit(event: "invoice.voided")
  class Client
    DEFAULT_BASE_URL = "https://api.santati.io"
    DEFAULT_TIMEOUT_MS = 10_000
    DEFAULT_MAX_RETRIES = 2
    DEFAULT_INITIAL_BACKOFF_MS = 250
    DEFAULT_MAX_BACKOFF_MS = 8000
    DEFAULT_BATCH_SIZE = 100
    DEFAULT_FLUSH_INTERVAL_MS = 1000

    attr_reader :trail, :timeout_ms, :max_retries, :initial_backoff_ms, :max_backoff_ms, :events

    def initialize(api_key:, base_url: DEFAULT_BASE_URL, trail: nil, timeout_ms: DEFAULT_TIMEOUT_MS,
      max_retries: DEFAULT_MAX_RETRIES, initial_backoff_ms: DEFAULT_INITIAL_BACKOFF_MS,
      max_backoff_ms: DEFAULT_MAX_BACKOFF_MS, headers: nil, outbox: nil, batch_size: DEFAULT_BATCH_SIZE,
      flush_interval_ms: DEFAULT_FLUSH_INTERVAL_MS, pre_send: nil, post_send: nil)
      raise ValidationError.new("api_key must be a non-empty string", field: "api_key") if api_key.to_s.empty?

      unless batch_size.is_a?(Integer) && batch_size.between?(1, 500)
        raise ValidationError.new("batch_size must be between 1 and 500", field: "batch_size")
      end
      unless flush_interval_ms.is_a?(Numeric) && flush_interval_ms > 0
        raise ValidationError.new("flush_interval_ms must be greater than 0", field: "flush_interval_ms")
      end

      headers ||= {}
      reject_authorization_header!(headers)

      @trail = trail
      @timeout_ms = timeout_ms
      @max_retries = max_retries
      @initial_backoff_ms = initial_backoff_ms
      @max_backoff_ms = max_backoff_ms
      @api = SantatiCore::AuditEventsApi.new(build_api_client(api_key, base_url, headers))
      @events = Events.new(self)
      @outbox = Outbox.new(
        self, store: outbox || MemoryOutbox.new, batch_size: batch_size,
        flush_interval_ms: flush_interval_ms, pre_send: pre_send, post_send: post_send
      )
    end

    # Validate like `events.emit`, store the event in the outbox and return its
    # idempotency key. Never makes a request: a background worker sends the
    # outbox in batches. Raises {OutboxError} when the store refuses or fails,
    # or once the client is closed.
    def log(event:, trail: nil, organization_id: nil, actor: nil, targets: nil, metadata: nil,
      data: nil, context: nil, created_at: nil, idempotency_key: nil)
      @outbox.log(@events.prepare(
        event: event, trail: trail, organization_id: organization_id, actor: actor,
        targets: targets, metadata: metadata, data: data, context: context,
        created_at: created_at, idempotency_key: idempotency_key
      ))
    end

    # Run one outbox pass now. Raises {OutboxError} when the store fails.
    def flush
      @outbox.flush
      nil
    end

    # Stop the worker, flush what is pending and refuse further `log` calls.
    # Calling it again does nothing.
    def close
      @outbox.close
      nil
    end

    # @api private
    attr_reader :api

    private

    def reject_authorization_header!(headers)
      headers.each_key do |key|
        next unless key.to_s.casecmp("authorization").zero?

        raise ValidationError.new("headers must not set authorization", field: "headers")
      end
    end

    def build_api_client(api_key, base_url, headers)
      uri = URI(base_url.sub(%r{/+\z}, ""))
      config = SantatiCore::Configuration.new
      config.scheme = uri.scheme
      config.host = "#{uri.host}:#{uri.port}"
      config.base_path = uri.path
      # The generated core applies the spec's ApiKeyAuth scheme from these two.
      config.api_key["Authorization"] = api_key
      config.api_key_prefix["Authorization"] = "Api-Key"
      config.timeout = timeout_ms / 1000.0

      # Configured before the first request: the generated core memoizes its
      # Faraday connection, and every setting below has to be in place by then.
      api_client = SantatiCore::ApiClient.new(config)
      api_client.user_agent = "santati-ruby/#{VERSION}"
      api_client.default_headers.merge!(headers.transform_keys(&:to_s))
      api_client
    end
  end
end
