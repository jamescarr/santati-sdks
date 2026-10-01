# frozen_string_literal: true

require "santati_core"

require_relative "santati/version"
require_relative "santati/errors"
require_relative "santati/results"
require_relative "santati/retry"
require_relative "santati/client"
require_relative "santati/events"

# Official Ruby SDK for the Santati audit-log API.
#
#   client = Santati::Client.new(api_key: "sat_sk_…", trail: "billing")
#   client.events.emit(event: "invoice.voided")
#   client.events.list(trail: "billing").results
#   client.events.iterate(trail: "billing").each { |event| puts event.id }
module Santati
  # The generated read models, re-exported so callers never have to reach into
  # `SantatiCore`.
  AuditEvent = SantatiCore::AuditEvent
  EventActor = SantatiCore::EventActor
  EventTarget = SantatiCore::EventTarget
end
