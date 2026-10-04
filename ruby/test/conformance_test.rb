# frozen_string_literal: true

require_relative "test_helper"
require "json"
require "webrick"

# Runs the language-neutral vectors in `conformance/` against this SDK: one
# test per vector, named by its `id`. See `conformance/README.md` for the
# vector format and the runner contract every SDK follows.
CASES_DIR = File.expand_path("../../conformance/cases", __dir__)
CASES = Dir[File.join(CASES_DIR, "*.json")].sort.flat_map { |path| JSON.parse(File.read(path)).fetch("cases") }
raise "no conformance cases found in #{CASES_DIR}" if CASES.empty?

ERROR_KINDS = {
  "ValidationError" => Santati::ValidationError,
  "AuthError" => Santati::AuthError,
  "NotFoundError" => Santati::NotFoundError,
  "RateLimitedError" => Santati::RateLimitedError,
  "ServerError" => Santati::ServerError,
  "TransportError" => Santati::TransportError,
  "ApiError" => Santati::ApiError,
  "OutboxError" => Santati::OutboxError
}.freeze

# A real HTTP server standing in for the API: it answers every request with
# the vector's gateway spec and records what it saw.
class ConformanceGateway
  UNREACHABLE = "http://127.0.0.1:1"

  attr_reader :base_url, :requests

  def initialize(spec)
    @responses = spec.key?("sequence") ? spec.fetch("sequence") : [spec]
    @requests = []
    @server = WEBrick::HTTPServer.new(
      BindAddress: "127.0.0.1",
      Port: 0,
      Logger: WEBrick::Log.new(File::NULL),
      AccessLog: []
    )
    @base_url = "http://127.0.0.1:#{@server.listeners.first.addr[1]}"
    @server.mount_proc("/") { |request, response| handle(request, response) }
    @thread = Thread.new { @server.start }
    wait_until_running
  end

  def stop
    @server.shutdown
    @thread.join(5) || @thread.kill
  end

  private

  # WEBrick only leaves its accept loop when `shutdown` lands after `start`
  # began; without this wait a case that makes no request could stop the
  # server while `start` is still on its way to the loop.
  def wait_until_running
    1000.times do
      return if @server.status == :Running

      sleep 0.001
    end
    raise "the conformance gateway did not start"
  end

  def handle(request, response)
    body = request.body
    @requests << {
      "method" => request.request_method,
      "path" => request.unparsed_uri,
      "headers" => request.header.transform_values(&:first),
      "body" => (body.nil? || body.empty?) ? nil : JSON.parse(body)
    }

    spec = @responses[[@requests.length - 1, @responses.length - 1].min]
    sleep(spec.fetch("delay_ms") / 1000.0) if spec.key?("delay_ms")
    response.status = spec.fetch("status")
    spec.fetch("headers", {}).each { |name, value| response[name] = value }
    write_body(response, spec["body"])
  rescue => e
    warn "conformance gateway: #{e.class}: #{e.message}"
  end

  def write_body(response, body)
    return if body.nil?

    if body.key?("json")
      response["content-type"] ||= "application/json"
      response.body = JSON.generate(body["json"])
    elsif body.key?("text")
      response["content-type"] ||= "text/plain; charset=utf-8"
      response.body = body["text"]
    end
  end
end

class ConformanceTest < Minitest::Test
  def run_case(test_case)
    input = test_case.fetch("input")
    with_gateway(input.fetch("gateway")) do |base_url, requests|
      result = nil
      error = nil
      outcomes = []
      begin
        client = build_client(
          input.fetch("client"), base_url, input["hooks"], outcomes,
          outbox: test_case.fetch("operation") == "emit_outbox"
        )
        result = perform(client, test_case.fetch("operation"), input, outcomes)
      rescue Santati::Error => e
        error = e
      end

      bindings = {}
      expect = test_case.fetch("expect")
      if expect.key?("ok")
        refute error, "expected success, got #{error.class}: #{error&.message}"
        assert_match_value(expect.fetch("ok"), result, bindings)
      else
        expected = expect.fetch("error")
        assert error, "expected #{expected.fetch("kind")}, got #{result.inspect}"
        assert_error(expected, error)
      end
      assert_requests(expect["requests"], requests, bindings) if expect.key?("requests")
    end
  end

  private

  def with_gateway(spec)
    if spec["unreachable"]
      yield ConformanceGateway::UNREACHABLE, []
      return
    end

    gateway = ConformanceGateway.new(spec)
    begin
      yield gateway.base_url, gateway.requests
    ensure
      gateway.stop
    end
  end

  def build_client(spec, base_url, hooks, outcomes, outbox:)
    options = {
      api_key: spec.fetch("api_key"),
      base_url: base_url + spec.fetch("base_path", ""),
      headers: spec.fetch("headers", {})
    }
    options[:trail] = spec["trail"] if spec.key?("trail")
    %w[timeout_ms max_retries initial_backoff_ms max_backoff_ms].each do |name|
      options[name.to_sym] = spec[name] if spec.key?(name)
    end
    %w[batch_size flush_interval_ms].each do |name|
      options[name.to_sym] = spec[name] if spec.key?(name)
    end
    if outbox
      options[:outbox] = spec.key?("max_pending") ? Santati::MemoryOutbox.new(max_pending: spec["max_pending"]) : Santati::MemoryOutbox.new
    end
    options[:pre_send] = pre_send_hook(hooks["pre_send"]) if hooks&.key?("pre_send")
    options[:post_send] = post_send_hook(hooks&.dig("post_send"), outcomes)
    Santati::Client.new(**options)
  end

  def pre_send_hook(spec)
    lambda do |event|
      raise "conformance pre_send" if spec["raise"]
      next nil if spec.fetch("drop_events", []).include?(event[:event])
      next event unless spec.key?("set_metadata")

      event.merge(metadata: (event[:metadata] || {}).merge(spec["set_metadata"].transform_keys(&:to_sym)))
    end
  end

  def post_send_hook(spec, outcomes)
    lambda do |event, outcome|
      error = outcome.error
      outcomes << {
        "event" => JSON.parse(JSON.generate(event)),
        "status" => outcome.status,
        "id" => outcome.id,
        "error" => error && {
          "kind" => ERROR_KINDS.key(error.class),
          "status" => error.status,
          "code" => error.code,
          "field" => error.field,
          "retry_after" => error.retry_after
        }
      }
      raise "conformance post_send" if spec && spec["raise"]
    end
  end

  def perform(client, operation, input, outcomes)
    case operation
    when "emit_outbox"
      emit_outbox_ok(client, input.fetch("events"), outcomes)
    when "emit"
      emit_ok(client.events.emit(**symbolize(input.fetch("event"))))
    when "emit_batch"
      batch_ok(client.events.emit_batch(input.fetch("events")))
    when "list"
      page_ok(client.events.list(**symbolize(input.fetch("params", {}))))
    when "iterate"
      client.events.iterate(**symbolize(input.fetch("params", {}))).map { |event| wire(event) }
    else
      raise "unknown operation #{operation.inspect}"
    end
  end

  # Emits every event through the outbox, then closes the client; an SDK error
  # stops the emitting and is raised once the client is closed.
  def emit_outbox_ok(client, events, outcomes)
    results = []
    error = nil
    begin
      events.each { |event| results << emit_ok(client.events.emit(**symbolize(event))) }
    rescue Santati::Error => e
      error = e
    ensure
      client.close
    end
    raise error if error

    {"results" => results, "outcomes" => outcomes}
  end

  def emit_ok(result)
    {
      "event" => result.event && wire(result.event),
      "duplicate" => result.duplicate,
      "idempotency_key" => result.idempotency_key,
      "queued" => result.queued
    }
  end

  def batch_ok(result)
    {
      "accepted" => result.accepted,
      "rejected" => result.rejected,
      "results" => result.results.map do |item|
        {
          "index" => item.index,
          "status" => item.status,
          "id" => item.id,
          "error" => item.error && {
            "code" => item.error.code,
            "message" => item.error.message,
            "field" => item.error.field
          }
        }
      end
    }
  end

  def page_ok(page)
    {
      "results" => page.results.map { |event| wire(event) },
      "next_cursor" => page.next_cursor
    }
  end

  # A generated read model, back to wire (snake_case) JSON.
  def wire(model)
    JSON.parse(JSON.generate(model.to_hash))
  end

  def symbolize(hash)
    hash.transform_keys(&:to_sym)
  end

  def assert_error(expected, error)
    klass = ERROR_KINDS.fetch(expected.fetch("kind"))
    assert_equal klass, error.class, "expected #{expected.fetch("kind")}, got #{error.class}: #{error.message}"
    %w[status code field retry_after].each do |key|
      next unless expected.key?(key)

      actual = error.public_send(key)
      if expected[key].nil?
        assert_nil actual, "#{key} differs"
      else
        assert_equal expected[key], actual, "#{key} differs"
      end
    end
  end

  def assert_requests(expected, requests, bindings)
    assert_equal expected.length, requests.length, "request count differs"
    expected.each_with_index do |want, index|
      got = requests[index]
      assert_equal want.fetch("method"), got.fetch("method"), "request #{index} method differs"
      assert_equal want.fetch("path"), got.fetch("path"), "request #{index} path differs"
      want.fetch("headers", {}).each do |name, value|
        assert_equal value, got.fetch("headers")[name.downcase], "request #{index} header #{name} differs"
      end
      next unless want.key?("body")

      assert_match_value(want.fetch("body"), got.fetch("body"), bindings, strip_nulls: false)
    end
  end

  # Deep equality after removing every object member whose value is null, with
  # `{"$generated": "<label>"}` matching any non-empty string.
  def assert_match_value(expected, actual, bindings, strip_nulls: true)
    case expected
    when Hash
      return assert_generated(expected, actual, bindings) if expected.keys == ["$generated"]

      expected = strip_nulls(expected) if strip_nulls
      actual = strip_nulls(actual) if strip_nulls
      assert actual.is_a?(Hash), "expected an object, got #{actual.inspect}"
      assert_equal expected.keys.sort, actual.keys.sort, "object members differ"
      expected.each { |key, value| assert_match_value(value, actual[key], bindings, strip_nulls: strip_nulls) }
    when Array
      assert actual.is_a?(Array), "expected an array, got #{actual.inspect}"
      assert_equal expected.length, actual.length, "array length differs"
      expected.each_with_index do |value, index|
        assert_match_value(value, actual[index], bindings, strip_nulls: strip_nulls)
      end
    else
      assert_equal expected, actual
    end
  end

  def assert_generated(expected, actual, bindings)
    label = expected.fetch("$generated")
    assert actual.is_a?(String) && !actual.empty?, "label #{label.inspect} must bind a non-empty string"
    if bindings.key?(label)
      assert_equal bindings[label], actual, "label #{label.inspect} bound to two different values"
    else
      refute bindings.value?(actual), "value #{actual.inspect} bound to two labels"
      bindings[label] = actual
    end
  end

  def strip_nulls(value)
    case value
    when Hash
      value.each_with_object({}) do |(key, member), stripped|
        member = strip_nulls(member)
        stripped[key] = member unless member.nil?
      end
    when Array
      value.map { |member| strip_nulls(member) }
    else
      value
    end
  end
end

CASES.each do |test_case|
  ConformanceTest.define_method("test_#{test_case.fetch("id")}") do
    run_case(test_case)
  end
end
