# frozen_string_literal: true

require "santati_core"

require_relative "santati/version"
require_relative "santati/errors"
require_relative "santati/results"
require_relative "santati/retry"
require_relative "santati/core_calls"
require_relative "santati/client"
require_relative "santati/events"
require_relative "santati/schemas"
require_relative "santati/outbox"

# Official Ruby SDK for the Santati audit-log API.
#
#   client = Santati::Client.new(api_key: "sat_sk_…", trail: "billing")
#   client.events.emit(event: "invoice.voided")
#   client.events.list(trail: "billing").results
#   client.events.iterate(trail: "billing").each { |event| puts event.id }
#   client.schemas.create_definition(action: "invoice.voided")
module Santati
  # The generated read models, re-exported so callers never have to reach into
  # `SantatiCore`.
  AuditEvent = SantatiCore::AuditEvent
  EventActor = SantatiCore::EventActor
  EventTarget = SantatiCore::EventTarget
  EventDefinition = SantatiCore::EventDefinition
  EventSchemaVersion = SantatiCore::EventSchemaVersion
  SchemaCheck = SantatiCore::SchemaCheck
  SchemaCheckFailure = SantatiCore::SchemaCheckFailure
  StandardEventCatalog = SantatiCore::StandardEventCatalog
  StandardPack = SantatiCore::StandardPack
  StandardEvent = SantatiCore::StandardEvent
  OcsfMapping = SantatiCore::OcsfMapping
  StandardPackInstallResult = SantatiCore::StandardPackInstallResult
end
