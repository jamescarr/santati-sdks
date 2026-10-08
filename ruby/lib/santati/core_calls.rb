# frozen_string_literal: true

require "json"
require "uri"

module Santati
  # What the resources that call the generated core share: error translation,
  # request-model construction, body decoding and the cursor reader. Included by
  # {Events} and {Schemas}.
  #
  # @api private
  module CoreCalls
    private

    # The cursor query parameter of a `next` URL, decoded, or `nil` when the
    # URL is absent or carries none.
    def cursor_from(next_url)
      return nil if next_url.nil? || next_url.empty?

      query = URI.decode_www_form(URI.parse(next_url).query.to_s).to_h
      query["cursor"]
    rescue URI::InvalidURIError
      nil
    end

    def compact(parameters)
      parameters.reject { |_, value| value.nil? }
    end

    # Runs the generated core and translates its `ApiError` into the facade's
    # own error kinds. A required parameter the generated core refuses (a nil
    # `version`, say) raises `ArgumentError` before any request: a local
    # validation failure.
    def generated
      yield
    rescue SantatiCore::ApiError => e
      raise Santati.from_api_error(e)
    rescue ArgumentError => e
      raise ValidationError.new(e.message)
    end

    # A generated request model rejecting a spec constraint (a length, an
    # enum, a missing required member) is a local validation failure.
    def build_model
      yield
    rescue ArgumentError => e
      raise ValidationError.new(e.message)
    end

    # Parse a 2xx body and hand the Hash to the block. Anything that is not a
    # JSON object, or that the generated model refuses, is an `ApiError`.
    def decode_body(body, status)
      parsed = body.nil? ? nil : JSON.parse(body)
      raise ApiError.new("could not decode the response body", status: status) unless parsed.is_a?(Hash)

      yield parsed
    rescue JSON::ParserError, ArgumentError
      raise ApiError.new("could not decode the response body", status: status)
    end
  end
end
