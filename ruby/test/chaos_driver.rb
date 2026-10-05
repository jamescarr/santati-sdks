# frozen_string_literal: true

# Chaos driver: emits into the outbox against the gateway in SANTATI_CHAOS and
# prints one CHAOS_RESULT line. Run by `mise run chaos ruby` (chaos/run.mjs);
# inert unless SANTATI_CHAOS is set.

require "json"
require "santati"

def now_ms = Process.clock_gettime(Process::CLOCK_MONOTONIC) * 1000

def percentile(sorted, q)
  return 0.0 if sorted.empty?

  sorted[[(q * sorted.length).ceil - 1, 0].max]
end

def error_key(error)
  kind = error.class.name.split("::").last
  error.is_a?(Santati::Error) ? "#{kind}:#{error.code}" : "#{kind}:unexpected"
end

raw = ENV["SANTATI_CHAOS"]
if raw.nil? || raw.empty?
  puts "SANTATI_CHAOS not set; skipping"
  exit 0
end

cfg = JSON.parse(raw)
opts = cfg["client"]

client = Santati::Client.new(
  api_key: "sat_sk_chaos",
  base_url: opts["base_url"],
  trail: "chaos",
  timeout_ms: opts["timeout_ms"],
  max_retries: opts["max_retries"],
  initial_backoff_ms: opts["initial_backoff_ms"],
  max_backoff_ms: opts["max_backoff_ms"],
  batch_size: opts["batch_size"],
  flush_interval_ms: opts["flush_interval_ms"],
  outbox: Santati::MemoryOutbox.new(max_pending: opts["max_pending"])
)

# Heartbeat: how late a sleep of heartbeat_ms wakes up.
heartbeat_s = cfg["heartbeat_ms"] / 1000.0
max_lag = 0.0
heartbeat_running = true
heartbeat = Thread.new do
  while heartbeat_running
    started = now_ms
    sleep(heartbeat_s)
    lag = now_ms - started - cfg["heartbeat_ms"]
    max_lag = lag if lag > max_lag
  end
end

# Flusher.
flush_calls = 0
flushing = true
flusher = if cfg["flusher_interval_ms"] > 0
  Thread.new do
    while flushing
      sleep(cfg["flusher_interval_ms"] / 1000.0)
      break unless flushing

      flush_calls += 1
      begin
        client.flush
      rescue Santati::Error
        nil
      end
    end
  end
end

# Emitters: one thread each, returning its own tally.
emitters = Array.new(cfg["emitters"]) do |index|
  Thread.new do
    tally = {latencies: [], queued: 0, errors: Hash.new(0)}
    cfg["events_per_emitter"].times do |seq|
      started = now_ms
      begin
        result = client.events.emit(event: "chaos.event", metadata: {"emitter" => index.to_s, "seq" => seq.to_s})
        tally[:queued] += 1 if result.queued
      rescue => e
        tally[:errors][error_key(e)] += 1
      end
      tally[:latencies] << now_ms - started
      sleep(cfg["emit_interval_ms"] / 1000.0) if cfg["emit_interval_ms"] > 0
    end
    tally
  end
end

tallies = []
caller_exits = 0
emitters.each do |thread|
  tallies << thread.value
rescue Exception # standard:disable Lint/RescueException
  caller_exits += 1
end

flushing = false
flusher&.join

errors = Hash.new(0)
tallies.each { |tally| tally[:errors].each { |key, n| errors[key] += n } }

close_started = now_ms
begin
  client.close
rescue Santati::Error => e
  errors[error_key(e)] += 1
end
close_ms = now_ms - close_started

heartbeat_running = false
heartbeat.join

latencies = tallies.flat_map { |tally| tally[:latencies] }.sort
result = {
  sdk: "ruby",
  emits: latencies.length,
  queued: tallies.sum { |tally| tally[:queued] },
  errors: errors,
  caller_exits: caller_exits,
  emit_ms: {
    p50: percentile(latencies, 0.50).round(3),
    p99: percentile(latencies, 0.99).round(3),
    max: (latencies.last || 0.0).round(3)
  },
  heartbeat_max_lag_ms: max_lag.round(3),
  close_ms: close_ms.round(3),
  flush_calls: flush_calls
}
puts "CHAOS_RESULT #{JSON.generate(result)}"
