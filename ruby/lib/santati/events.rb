# frozen_string_literal: true

require "json"
require "securerandom"
require "uri"

module Santati
  # The audit-event operations: emit one or a batch, list a page, iterate.
  #
  # Reach it through {Client#events}; every method raises a {Santati::Error}
  # subclass on failure, per `docs/sdk-surface.md`.
  class Events
    # @api private
    def initialize(client)
      @client = client
    end

    # Emit one event. Returns an {EmitResult}; with an `outbox:` the event is
    # stored for the background worker and `queued` is true.
    def emit(event:, trail: nil, organization_id: nil, actor: nil, targets: nil, metadata: nil,
      data: nil, context: nil, created_at: nil, idempotency_key: nil)
      if (outbox = @client.outbox)
        stored = prepare(
          event: event, trail: trail, organization_id: organization_id, actor: actor,
          targets: targets, metadata: metadata, data: data, context: context,
          created_at: created_at, idempotency_key: idempotency_key
        )
        outbox.enqueue(stored)
        return EmitResult.new(event: nil, duplicate: false, idempotency_key: stored.fetch(:idempotency_key), queued: true)
      end

      attributes = build_envelope(
        event: event, trail: trail, organization_id: organization_id, actor: actor,
        targets: targets, metadata: metadata, data: data, context: context,
        created_at: created_at, idempotency_key: idempotency_key, field_prefix: ""
      )
      request = build_model { SantatiCore::EventEnvelopeRequest.new(attributes) }
      key = attributes[:idempotency_key]

      Retry.call(@client) do
        body, status = generated { @client.api.events_create_with_http_info(request, debug_return_type: "String") }
        case status
        when 201 then EmitResult.new(event: decode_event(body, status), duplicate: false, idempotency_key: key, queued: false)
        when 200 then EmitResult.new(event: decode_event(body, status), duplicate: true, idempotency_key: key, queued: false)
        else raise ApiError.new("unexpected status #{status}", status: status)
        end
      end
    end

    # Validate like {#emit} and return the stored event for the outbox: a plain
    # symbol-keyed Hash with the trail and the idempotency key resolved.
    #
    # @api private
    def prepare(event:, trail: nil, organization_id: nil, actor: nil, targets: nil, metadata: nil,
      data: nil, context: nil, created_at: nil, idempotency_key: nil)
      stored = build_envelope(
        event: event, trail: trail, organization_id: organization_id, actor: actor,
        targets: targets, metadata: metadata, data: data, context: context,
        created_at: created_at, idempotency_key: idempotency_key, field_prefix: ""
      )
      stored[:actor] = normalize_hash(actor) unless actor.nil?
      stored[:targets] = targets.map { |target| normalize_hash(target) } unless targets.nil?
      stored
    end

    # Emit a batch of events. Returns a {BatchResult}; a 207 is a result, not
    # an error.
    def emit_batch(events)
      emit_batch_with_status(events).last
    end

    # Like {#emit_batch}, but returns `[http_status, BatchResult]` (202 or 207).
    #
    # @api private
    def emit_batch_with_status(events)
      events = Array(events)
      raise ValidationError.new("events must be a non-empty list", field: "events") if events.empty?

      envelopes = events.each_with_index.map do |raw, index|
        item = normalize_event(raw)
        build_envelope(
          event: item[:event], trail: item[:trail], organization_id: item[:organization_id],
          actor: item[:actor], targets: item[:targets], metadata: item[:metadata],
          data: item[:data], context: item[:context], created_at: item[:created_at],
          idempotency_key: item[:idempotency_key], field_prefix: "events[#{index}]."
        )
      end
      request = build_model do
        SantatiCore::EventBatchRequest.new(
          events: envelopes.map { |attributes| SantatiCore::EventEnvelopeRequest.new(attributes) }
        )
      end

      Retry.call(@client) do
        body, status = generated { @client.api.events_create_with_http_info(request, debug_return_type: "String") }
        case status
        when 202, 207 then [status, decode_batch(body, status)]
        else raise ApiError.new("unexpected status #{status}", status: status)
        end
      end
    end

    # Read one page of audit events. Returns an {EventPage}.
    def list(trail: nil, event: nil, event_prefix: nil, organization_id: nil, actor_id: nil,
      actor_type: nil, target_type: nil, target_id: nil, created_after: nil,
      created_before: nil, q: nil, sort: nil, limit: nil, cursor: nil)
      fetch_page(compact(
        trail: trail, event: event, event_prefix: event_prefix, organization_id: organization_id,
        actor_id: actor_id, actor_type: actor_type, target_type: target_type, target_id: target_id,
        created_after: created_after, created_before: created_before, q: q, sort: sort,
        limit: limit, cursor: cursor
      ))
    end

    # Lazily walk every page, yielding `SantatiCore::AuditEvent`s. An error on
    # a later page is raised after the events of the earlier pages were
    # yielded.
    def iterate(trail: nil, event: nil, event_prefix: nil, organization_id: nil, actor_id: nil,
      actor_type: nil, target_type: nil, target_id: nil, created_after: nil,
      created_before: nil, q: nil, sort: nil, limit: nil)
      parameters = compact(
        trail: trail, event: event, event_prefix: event_prefix, organization_id: organization_id,
        actor_id: actor_id, actor_type: actor_type, target_type: target_type, target_id: target_id,
        created_after: created_after, created_before: created_before, q: q, sort: sort, limit: limit
      )

      Enumerator.new do |yielder|
        page = fetch_page(parameters)
        loop do
          page.results.each { |audit_event| yielder << audit_event }
          break if page.next_cursor.nil?

          page = fetch_page(parameters.merge(cursor: page.next_cursor))
        end
      end
    end

    private

    def fetch_page(parameters)
      Retry.call(@client) do
        body, status = generated { @client.api.events_list_with_http_info(parameters.merge(debug_return_type: "String")) }
        raise ApiError.new("unexpected status #{status}", status: status) unless status == 200

        page = decode_body(body, status) { |parsed| SantatiCore::PaginatedAuditEventList.build_from_hash(parsed) }
        EventPage.new(results: page.results || [], next_cursor: cursor_from(page._next))
      end
    end

    # The cursor query parameter of a `next` URL, decoded, or `nil` when the
    # URL is absent or carries none.
    def cursor_from(next_url)
      return nil if next_url.nil? || next_url.empty?

      query = URI.decode_www_form(URI.parse(next_url).query.to_s).to_h
      query["cursor"]
    rescue URI::InvalidURIError
      nil
    end

    # One envelope's attributes: only the supplied members, trail resolved,
    # a generated idempotency key when the caller sent none.
    def build_envelope(event:, trail:, organization_id:, actor:, targets:, metadata:, data:,
      context:, created_at:, idempotency_key:, field_prefix:)
      event = event.to_s
      raise ValidationError.new("event must be a non-empty string", field: "#{field_prefix}event") if event.empty?

      trail = resolve_trail(trail)
      if trail.nil? || trail.to_s.empty?
        raise ValidationError.new("trail must be a non-empty string", field: "#{field_prefix}trail")
      end

      envelope = {event: event, trail: trail}
      envelope[:organization_id] = organization_id unless organization_id.nil?
      envelope[:created_at] = created_at unless created_at.nil?
      envelope[:idempotency_key] = idempotency_key.nil? ? SecureRandom.uuid : idempotency_key.to_s
      envelope[:actor] = build_model { SantatiCore::EventActorRequest.new(normalize_hash(actor)) } unless actor.nil?
      unless targets.nil?
        envelope[:targets] = targets.map { |target| build_model { SantatiCore::EventTargetRequest.new(normalize_hash(target)) } }
      end
      envelope[:metadata] = metadata unless metadata.nil?
      envelope[:data] = data unless data.nil?
      envelope[:context] = context unless context.nil?
      envelope
    end

    def resolve_trail(trail)
      (trail.nil? || trail.to_s.empty?) ? @client.trail : trail
    end

    def normalize_event(item)
      raise ValidationError.new("event must be a hash") unless item.is_a?(Hash)

      item.transform_keys(&:to_sym)
    end

    def normalize_hash(value)
      raise ValidationError.new("value must be a hash") unless value.is_a?(Hash)

      value.transform_keys(&:to_sym)
    end

    def compact(parameters)
      parameters.reject { |_, value| value.nil? }
    end

    # Runs the generated core and translates its `ApiError` into the facade's
    # own error kinds.
    def generated
      yield
    rescue SantatiCore::ApiError => e
      raise Santati.from_api_error(e)
    end

    # A generated request model rejecting a spec constraint (a length, an
    # enum, a missing required member) is a local validation failure.
    def build_model
      yield
    rescue ArgumentError => e
      raise ValidationError.new(e.message)
    end

    def decode_event(body, status)
      decode_body(body, status) { |parsed| SantatiCore::AuditEvent.build_from_hash(parsed) }
    end

    def decode_batch(body, status)
      decode_body(body, status) do |parsed|
        result = SantatiCore::EventBatchResult.build_from_hash(parsed)
        BatchResult.new(
          accepted: result.accepted,
          rejected: result.rejected,
          results: (result.results || []).map { |item| batch_item(item) }
        )
      end
    end

    def batch_item(item)
      BatchItem.new(
        index: item.index,
        status: item.status,
        id: item.id,
        error: item.error && BatchItemError.new(
          code: item.error.code, message: item.error.message, field: item.error.field
        )
      )
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
