# frozen_string_literal: true

module Santati
  # Event definitions, their schema versions and the standard packs.
  #
  # Reach it through {Client#schemas}; every method raises a {Santati::Error}
  # subclass on failure, per `docs/sdk-surface.md`.
  #
  # An empty `action` raises {ValidationError} (field `action`) before any
  # request; everything else is forwarded as given and the server decides.
  # Every operation but {#create_version} retries like {Events}; `create_version`
  # is sent once, because a repeat would create a second draft. Any 2xx status
  # but the one an operation expects, and a 2xx body that does not decode, raise
  # {ApiError}.
  class Schemas
    include CoreCalls

    # @api private
    def initialize(client)
      @client = client
    end

    # Read one page of the team's event definitions. Returns a {DefinitionPage}.
    def list_definitions(limit: nil, cursor: nil)
      fetch_definition_page(compact(cursor: cursor, limit: limit))
    end

    # Lazily walk every definition, page by page, yielding
    # `SantatiCore::EventDefinition`s. An error on a later page is raised after
    # the definitions of the earlier pages were yielded.
    def iterate_definitions(limit: nil)
      iterate_pages(compact(limit: limit)) { |parameters| fetch_definition_page(parameters) }
    end

    # Read one event definition.
    def get_definition(action)
      require_action(action)
      body, status, = perform(200) do
        api.event_definitions_retrieve_with_http_info(action, debug_return_type: "String")
      end
      decode_definition(body, status)
    end

    # Define an action; only the members you supply are sent.
    def create_definition(action:, description: nil, allowed_target_types: nil, is_active: nil)
      require_action(action)
      request = build_model do
        SantatiCore::EventDefinitionWriteRequest.new(
          compact(action: action, description: description,
            allowed_target_types: allowed_target_types, is_active: is_active)
        )
      end
      body, status, = perform(201) do
        api.event_definitions_create_with_http_info(request, debug_return_type: "String")
      end
      decode_definition(body, status)
    end

    # Change a definition; only the members you supply are sent, and
    # `new_action` renames the action.
    def update_definition(action, new_action: nil, description: nil, allowed_target_types: nil, is_active: nil)
      require_action(action)
      request = build_model do
        SantatiCore::PatchedEventDefinitionWriteRequest.new(
          compact(action: new_action, description: description,
            allowed_target_types: allowed_target_types, is_active: is_active)
        )
      end
      body, status, = perform(200) do
        api.event_definitions_update_with_http_info(
          action, patched_event_definition_write_request: request, debug_return_type: "String"
        )
      end
      decode_definition(body, status)
    end

    # Delete an event definition. Returns `nil`.
    def delete_definition(action)
      require_action(action)
      perform(204) { api.event_definitions_destroy_with_http_info(action, debug_return_type: "String") }
      nil
    end

    # Read one page of an action's schema versions, newest first. Returns a
    # {SchemaVersionPage}.
    def list_versions(action, limit: nil, cursor: nil)
      require_action(action)
      fetch_version_page(action, compact(cursor: cursor, limit: limit))
    end

    # Lazily walk every schema version of an action, page by page, yielding
    # `SantatiCore::EventSchemaVersion`s; see {#iterate_definitions}.
    def iterate_versions(action, limit: nil)
      require_action(action)
      iterate_pages(compact(limit: limit)) { |parameters| fetch_version_page(action, parameters) }
    end

    # Read one schema version with its `ETag`. Returns a {SchemaVersionResult}.
    def get_version(action, version)
      require_action(action)
      version_result(*perform(200) do
        api.schema_versions_retrieve_with_http_info(action, version, debug_return_type: "String")
      end)
    end

    # Create a draft from a JSON Schema document. Sent once: it is never retried.
    def create_version(action, schema)
      require_action(action)
      request = document(schema)
      version_result(*perform(201, retries: false) do
        api.schema_versions_create_with_http_info(action, request, debug_return_type: "String")
      end)
    end

    # Replace a draft's document. With `if_match` (an `ETag` you read) a
    # concurrent edit raises {ApiError} with status 412 instead of being
    # overwritten.
    def update_version(action, version, schema, if_match: nil)
      require_action(action)
      request = document(schema)
      version_result(*perform(200) do
        api.schema_versions_update_with_http_info(
          action, version, request, debug_return_type: "String", if_match: if_match
        )
      end)
    end

    # Delete a draft; a published version raises {ApiError} with status 409.
    # Returns `nil`.
    def delete_version(action, version)
      require_action(action)
      perform(204) { api.schema_versions_destroy_with_http_info(action, version, debug_return_type: "String") }
      nil
    end

    # Publish a draft: it becomes immutable and validates ingest.
    def publish_version(action, version)
      require_action(action)
      version_result(*perform(200) do
        api.schema_versions_publish_with_http_info(action, version, debug_return_type: "String")
      end)
    end

    # Start the migration window of a superseded version.
    def deprecate_version(action, version)
      require_action(action)
      version_result(*perform(200) do
        api.schema_versions_deprecate_with_http_info(action, version, debug_return_type: "String")
      end)
    end

    # Dry-run a document against the action's newest stored events; nothing is
    # stored. Returns a `SantatiCore::SchemaCheck`.
    def check_schema(action, schema)
      require_action(action)
      request = document(schema)
      body, status, = perform(200) do
        api.schema_versions_check_with_http_info(action, request, debug_return_type: "String")
      end
      decode_body(body, status) { |parsed| SantatiCore::SchemaCheck.build_from_hash(parsed) }
    end

    # Read the standard catalog: every pack and its actions. Returns a
    # `SantatiCore::StandardEventCatalog`.
    def list_standard_packs
      body, status, = perform(200) { api.standard_events_list_with_http_info(debug_return_type: "String") }
      decode_body(body, status) { |parsed| SantatiCore::StandardEventCatalog.build_from_hash(parsed) }
    end

    # Install packs by slug; the slugs are forwarded unchanged and the server
    # judges them. Returns a `SantatiCore::StandardPackInstallResult`.
    def install_standard_packs(packs)
      request = build_model { SantatiCore::StandardPackInstallRequest.new(packs: packs) }
      body, status, = perform(200) do
        api.standard_events_install_with_http_info(request, debug_return_type: "String")
      end
      decode_body(body, status) { |parsed| SantatiCore::StandardPackInstallResult.build_from_hash(parsed) }
    end

    private

    def api
      @client.definitions_api
    end

    def require_action(action)
      return unless action.nil? || action.to_s.empty?

      raise ValidationError.new("action must be a non-empty string", field: "action")
    end

    # One operation: runs the generated call (retried unless `retries` is
    # false), requires the `expected` 2xx status and returns the generated
    # `[body, status, headers]`.
    def perform(expected, retries: true, &call)
      Retry.call(@client, max_retries: retries ? @client.max_retries : 0) do
        body, status, headers = generated(&call)
        raise ApiError.new("unexpected status #{status}", status: status) unless status == expected

        [body, status, headers]
      end
    end

    def document(schema)
      build_model { SantatiCore::EventSchemaDocumentRequest.new(schema: schema) }
    end

    def fetch_definition_page(parameters)
      body, status, = perform(200) do
        api.event_definitions_list_with_http_info(parameters.merge(debug_return_type: "String"))
      end
      page = decode_body(body, status) { |parsed| SantatiCore::PaginatedEventDefinitionList.build_from_hash(parsed) }
      DefinitionPage.new(results: page.results || [], next_cursor: cursor_from(page._next))
    end

    def fetch_version_page(action, parameters)
      body, status, = perform(200) do
        api.schema_versions_list_with_http_info(action, parameters.merge(debug_return_type: "String"))
      end
      page = decode_body(body, status) { |parsed| SantatiCore::PaginatedEventSchemaVersionList.build_from_hash(parsed) }
      SchemaVersionPage.new(results: page.results || [], next_cursor: cursor_from(page._next))
    end

    # An Enumerator over the results of a paged operation: `fetch` answers one
    # page for the given parameters.
    def iterate_pages(parameters, &fetch)
      Enumerator.new do |yielder|
        page = fetch.call(parameters)
        loop do
          page.results.each { |item| yielder << item }
          break if page.next_cursor.nil?

          page = fetch.call(parameters.merge(cursor: page.next_cursor))
        end
      end
    end

    def decode_definition(body, status)
      decode_body(body, status) { |parsed| SantatiCore::EventDefinition.build_from_hash(parsed) }
    end

    def version_result(body, status, headers)
      version = decode_body(body, status) { |parsed| SantatiCore::EventSchemaVersion.build_from_hash(parsed) }
      SchemaVersionResult.new(schema_version: version, etag: headers && headers["ETag"])
    end
  end
end
