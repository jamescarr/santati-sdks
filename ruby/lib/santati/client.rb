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

    attr_reader :trail, :timeout_ms, :max_retries, :initial_backoff_ms, :max_backoff_ms, :events

    def initialize(api_key:, base_url: DEFAULT_BASE_URL, trail: nil, timeout_ms: DEFAULT_TIMEOUT_MS,
      max_retries: DEFAULT_MAX_RETRIES, initial_backoff_ms: DEFAULT_INITIAL_BACKOFF_MS,
      max_backoff_ms: DEFAULT_MAX_BACKOFF_MS, headers: nil)
      raise ValidationError.new("api_key must be a non-empty string", field: "api_key") if api_key.to_s.empty?

      headers ||= {}
      reject_authorization_header!(headers)

      @trail = trail
      @timeout_ms = timeout_ms
      @max_retries = max_retries
      @initial_backoff_ms = initial_backoff_ms
      @max_backoff_ms = max_backoff_ms
      @api = SantatiCore::AuditEventsApi.new(build_api_client(api_key, base_url, headers))
      @events = Events.new(self)
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
      config.api_key["Authorization"] = api_key
      config.api_key_prefix["Authorization"] = "Api-Key"
      config.timeout = timeout_ms / 1000.0

      # Configured before the first request: the generated core memoizes its
      # Faraday connection, and every setting below has to be in place by then.
      api_client = SantatiCore::ApiClient.new(config)
      api_client.user_agent = "santati-ruby/#{VERSION}"
      api_client.default_headers.merge!(headers.transform_keys(&:to_s))
      api_client.default_headers["Authorization"] = "Api-Key #{api_key}"
      api_client
    end
  end
end
