# frozen_string_literal: true

require "json"

module Santati
  # Base class for everything the SDK raises.
  #
  # `status` is the HTTP status of the failed request, or `nil` for a local
  # validation failure or a transport failure. `code`, `field` and
  # `retry_after` come from the response body and the `Retry-After` header.
  class Error < StandardError
    attr_reader :status, :code, :field, :retry_after

    def initialize(message, status: nil, code: nil, field: nil, retry_after: nil)
      super(message)
      @status = status
      @code = code
      @field = field
      @retry_after = retry_after
    end
  end

  # A local validation failure, or an HTTP 400, 413 or 422.
  class ValidationError < Error; end

  # An HTTP 400, 413 or 422 whose code is `schema_validation_failed`: the event
  # broke the action's JSON Schema, named a disallowed target type, or pinned an
  # unusable `schema_version`. A {ValidationError}, so `rescue ValidationError`
  # still catches it.
  class SchemaValidationError < ValidationError; end

  # HTTP 401 or 403.
  class AuthError < Error; end

  # HTTP 404.
  class NotFoundError < Error; end

  # HTTP 429.
  class RateLimitedError < Error; end

  # HTTP 500-599.
  class ServerError < Error; end

  # No HTTP response at all: refused, DNS, TLS or timeout.
  class TransportError < Error; end

  # The outbox store refused or failed (`outbox_full`, `store_unavailable`,
  # `closed`), or a `pre_send` hook raised (`hook_failed`). `status` is `nil`.
  class OutboxError < Error; end

  # Any other non-2xx, an unexpected 2xx, or an undecodable 2xx body.
  class ApiError < Error; end

  # The error kind for an HTTP status, per `docs/sdk-surface.md`.
  def self.error_class_for(status)
    case status
    when 400, 413, 422 then ValidationError
    when 401, 403 then AuthError
    when 404 then NotFoundError
    when 429 then RateLimitedError
    when 500..599 then ServerError
    else ApiError
    end
  end

  # The kind of a 400, 413 or 422 whose body carried `code`.
  def self.validation_class_for(code)
    (code == "schema_validation_failed") ? SchemaValidationError : ValidationError
  end

  # Translate a failure raised by the generated core into a {Santati::Error}.
  # A generated `ApiError` without a status means the request never produced an
  # HTTP response, so it becomes a {TransportError}.
  def self.from_api_error(error)
    status = error.code
    return TransportError.new(error.message) if status.nil? || status.zero?

    status = status.to_i
    code, field, message = parse_error_body(error.response_body)
    error_class = error_class_for(status)
    error_class = validation_class_for(code) if error_class == ValidationError
    error_class.new(
      message || "HTTP #{status}",
      status: status,
      code: code,
      field: field,
      retry_after: retry_after(error.response_headers)
    )
  end

  # `[code, field, message]` from a response body, all `nil` when the body
  # carries none of the shapes the API documents.
  def self.parse_error_body(body)
    return [nil, nil, nil] if body.nil? || body.empty?

    parsed = begin
      JSON.parse(body)
    rescue JSON::ParserError
      nil
    end
    return [nil, nil, nil] unless parsed.is_a?(Hash)

    envelope = parsed["error"]
    if envelope.is_a?(Hash) && envelope["code"].is_a?(String)
      field = envelope["field"]
      return [envelope["code"], field.is_a?(String) ? field : nil, envelope["message"]]
    end

    detail = parsed["detail"]
    detail.is_a?(String) ? [nil, nil, detail] : [nil, nil, nil]
  end

  # Seconds from `Retry-After`, or `nil` when it is absent or not all digits.
  def self.retry_after(headers)
    value = headers && headers["Retry-After"]
    return nil if value.nil?

    value.to_s.match?(/\A\d+\z/) ? value.to_i : nil
  end
end
