defmodule Santati.Schemas do
  @moduledoc """
  Event definitions, their schema versions and the standard packs.

  A schema version is a JSON Schema 2020-12 document (`draft` → `published` →
  `deprecated`) that validates an action's `metadata`, `actor.metadata` and
  `targets[].metadata`.

  All functions take a `Santati.Client` first and answer `{:ok, value}` or
  `{:error, exception}` (`:ok` or `{:error, exception}` for the deletes).

  An empty or non-string `action` answers a `Santati.ValidationError` (field
  `action`) before any request; everything else is forwarded as given and the
  server decides. Every operation but `create_version/3` retries like
  `Santati.Events`; `create_version/3` is sent once, because a repeat would
  create a second draft. Any 2xx status but the one an operation expects, and a
  2xx body that does not decode, answer a `Santati.ApiError`.
  """

  alias Santati.{
    Client,
    DefinitionPage,
    Errors,
    SchemaVersionPage,
    SchemaVersionResult
  }

  alias SantatiCore.Deserializer
  alias SantatiCore.Model.EventDefinition
  alias SantatiCore.Model.EventSchemaVersion
  alias SantatiCore.Model.PaginatedEventDefinitionList
  alias SantatiCore.Model.PaginatedEventSchemaVersionList
  alias SantatiCore.Model.SchemaCheck
  alias SantatiCore.Model.StandardEventCatalog
  alias SantatiCore.Model.StandardPackInstallResult

  @definitions_path "/api/v0/event-definitions/"
  @standard_path "/api/v0/standard-events/"

  @page_parameters [:cursor, :limit]
  @definition_members ["action", "description", "allowed_target_types", "is_active"]
  @update_members ["new_action", "description", "allowed_target_types", "is_active"]

  @typedoc "Page parameters: a keyword list or a map, string or atom keys; `:limit` and `:cursor`."
  @type params :: keyword() | map()

  @typedoc "A JSON Schema document: a map, forwarded as given."
  @type schema :: map()

  ## Definitions

  @doc """
  Reads one page of the team's event definitions.

  `params` takes `:limit` and `:cursor`. Answers `{:ok, %Santati.DefinitionPage{}}`;
  `next_cursor` is the cursor decoded from the response's `next` URL, `nil` on
  the last page.
  """
  @spec list_definitions(Client.t(), params()) ::
          {:ok, DefinitionPage.t()} | {:error, Exception.t()}
  def list_definitions(%Client{} = client, params \\ []) do
    with {:ok, query} <- page_query(params) do
      call(client, :get, @definitions_path, 200, [query: query], fn response ->
        with {:ok, %PaginatedEventDefinitionList{} = page} <-
               decode(response, PaginatedEventDefinitionList) do
          {:ok,
           %DefinitionPage{
             results: page.results || [],
             next_cursor: Client.next_cursor(page.next)
           }}
        end
      end)
    end
  end

  @doc """
  Lazily iterates every event definition, following `next` cursors page by
  page (`params` takes `:limit`; a `:cursor` is ignored).

  Returns an `Enumerable` of `SantatiCore.Model.EventDefinition`. An error on a
  later page is raised after the earlier definitions were yielded.
  """
  @spec stream_definitions(Client.t(), params()) :: Enumerable.t()
  def stream_definitions(%Client{} = client, params \\ []) do
    stream_pages(params, fn page_params -> list_definitions(client, page_params) end)
  end

  @doc "Reads one event definition."
  @spec get_definition(Client.t(), String.t()) ::
          {:ok, EventDefinition.t()} | {:error, Exception.t()}
  def get_definition(%Client{} = client, action) do
    with :ok <- require_action(action) do
      call(client, :get, definition_path(action), 200, [], &decode(&1, EventDefinition))
    end
  end

  @doc """
  Defines an action.

  `definition` is a map (atom or string keys) with `action` required and
  `description`, `allowed_target_types` and `is_active` optional. Absent and
  `nil` members are never sent.
  """
  @spec create_definition(Client.t(), map() | keyword()) ::
          {:ok, EventDefinition.t()} | {:error, Exception.t()}
  def create_definition(%Client{} = client, definition) do
    members = members(definition, @definition_members)

    with :ok <- require_action(Map.get(members, "action")) do
      call(
        client,
        :post,
        @definitions_path,
        201,
        [body: members],
        &decode(&1, EventDefinition)
      )
    end
  end

  @doc """
  Changes a definition.

  `changes` is a map (atom or string keys) of `new_action` (sent as `action`,
  which renames the action), `description`, `allowed_target_types` and
  `is_active`. Absent and `nil` members are never sent, so `%{}` sends `{}`.
  """
  @spec update_definition(Client.t(), String.t(), map() | keyword()) ::
          {:ok, EventDefinition.t()} | {:error, Exception.t()}
  def update_definition(%Client{} = client, action, changes) do
    with :ok <- require_action(action) do
      body =
        changes
        |> members(@update_members)
        |> Map.new(fn
          {"new_action", value} -> {"action", value}
          other -> other
        end)

      call(
        client,
        :patch,
        definition_path(action),
        200,
        [body: body],
        &decode(&1, EventDefinition)
      )
    end
  end

  @doc "Deletes an event definition."
  @spec delete_definition(Client.t(), String.t()) :: :ok | {:error, Exception.t()}
  def delete_definition(%Client{} = client, action) do
    with :ok <- require_action(action) do
      nothing(
        call(client, :delete, definition_path(action), 204, [], fn _response -> {:ok, nil} end)
      )
    end
  end

  ## Schema versions

  @doc """
  Reads one page of an action's schema versions, newest first.

  `params` takes `:limit` and `:cursor`; see `list_definitions/2`.
  """
  @spec list_versions(Client.t(), String.t(), params()) ::
          {:ok, SchemaVersionPage.t()} | {:error, Exception.t()}
  def list_versions(%Client{} = client, action, params \\ []) do
    with :ok <- require_action(action),
         {:ok, query} <- page_query(params) do
      call(client, :get, versions_path(action), 200, [query: query], fn response ->
        with {:ok, %PaginatedEventSchemaVersionList{} = page} <-
               decode(response, PaginatedEventSchemaVersionList) do
          {:ok,
           %SchemaVersionPage{
             results: page.results || [],
             next_cursor: Client.next_cursor(page.next)
           }}
        end
      end)
    end
  end

  @doc """
  Lazily iterates every schema version of an action, following `next` cursors
  page by page; see `stream_definitions/2`.
  """
  @spec stream_versions(Client.t(), String.t(), params()) :: Enumerable.t()
  def stream_versions(%Client{} = client, action, params \\ []) do
    stream_pages(params, fn page_params -> list_versions(client, action, page_params) end)
  end

  @doc "Reads one schema version with its `ETag`."
  @spec get_version(Client.t(), String.t(), integer()) ::
          {:ok, SchemaVersionResult.t()} | {:error, Exception.t()}
  def get_version(%Client{} = client, action, version) do
    with :ok <- require_action(action) do
      call(client, :get, version_path(action, version), 200, [], &versioned/1)
    end
  end

  @doc "Creates a draft from a JSON Schema document. Sent once: it is never retried."
  @spec create_version(Client.t(), String.t(), schema()) ::
          {:ok, SchemaVersionResult.t()} | {:error, Exception.t()}
  def create_version(%Client{} = client, action, schema) do
    with :ok <- require_action(action) do
      call(
        client,
        :post,
        versions_path(action),
        201,
        [body: %{"schema" => schema}, retries: false],
        &versioned/1
      )
    end
  end

  @doc """
  Replaces a draft's document.

  With `if_match: etag` (an `ETag` you read) a concurrent edit answers a
  `Santati.ApiError` of status 412 instead of being overwritten; without it the
  last write wins.
  """
  @spec update_version(Client.t(), String.t(), integer(), schema(), keyword()) ::
          {:ok, SchemaVersionResult.t()} | {:error, Exception.t()}
  def update_version(%Client{} = client, action, version, schema, options \\ [])
      when is_list(options) do
    with :ok <- require_action(action) do
      headers =
        case Keyword.get(options, :if_match) do
          nil -> []
          etag -> [{"if-match", etag}]
        end

      call(
        client,
        :put,
        version_path(action, version),
        200,
        [body: %{"schema" => schema}, headers: headers],
        &versioned/1
      )
    end
  end

  @doc "Deletes a draft; a published version answers a `Santati.ApiError` of status 409."
  @spec delete_version(Client.t(), String.t(), integer()) :: :ok | {:error, Exception.t()}
  def delete_version(%Client{} = client, action, version) do
    with :ok <- require_action(action) do
      nothing(
        call(client, :delete, version_path(action, version), 204, [], fn _response ->
          {:ok, nil}
        end)
      )
    end
  end

  @doc "Publishes a draft: it becomes immutable and validates ingest."
  @spec publish_version(Client.t(), String.t(), integer()) ::
          {:ok, SchemaVersionResult.t()} | {:error, Exception.t()}
  def publish_version(%Client{} = client, action, version) do
    with :ok <- require_action(action) do
      call(client, :post, version_path(action, version) <> "publish/", 200, [], &versioned/1)
    end
  end

  @doc "Starts the migration window of a superseded version."
  @spec deprecate_version(Client.t(), String.t(), integer()) ::
          {:ok, SchemaVersionResult.t()} | {:error, Exception.t()}
  def deprecate_version(%Client{} = client, action, version) do
    with :ok <- require_action(action) do
      call(client, :post, version_path(action, version) <> "deprecate/", 200, [], &versioned/1)
    end
  end

  @doc "Dry-runs a document against the action's newest stored events; nothing is stored."
  @spec check_schema(Client.t(), String.t(), schema()) ::
          {:ok, SchemaCheck.t()} | {:error, Exception.t()}
  def check_schema(%Client{} = client, action, schema) do
    with :ok <- require_action(action) do
      call(
        client,
        :post,
        versions_path(action) <> "check/",
        200,
        [body: %{"schema" => schema}],
        &decode(&1, SchemaCheck)
      )
    end
  end

  ## Standard packs

  @doc "Reads the standard catalog: every pack and its actions."
  @spec list_standard_packs(Client.t()) ::
          {:ok, StandardEventCatalog.t()} | {:error, Exception.t()}
  def list_standard_packs(%Client{} = client) do
    call(client, :get, @standard_path, 200, [], &decode(&1, StandardEventCatalog))
  end

  @doc "Installs packs by slug; the slugs are forwarded unchanged and the server judges them."
  @spec install_standard_packs(Client.t(), [String.t()]) ::
          {:ok, StandardPackInstallResult.t()} | {:error, Exception.t()}
  def install_standard_packs(%Client{} = client, packs) do
    call(
      client,
      :post,
      @standard_path <> "install/",
      200,
      [body: %{"packs" => packs}],
      &decode(&1, StandardPackInstallResult)
    )
  end

  ## Plumbing

  # One operation: the `expected` 2xx status is the only success, `result` reads
  # the answer, and any other 2xx is an `Santati.ApiError`.
  defp call(client, method, path, expected, options, result) do
    case Client.request(client, method, path, options) do
      {:ok, %{status: ^expected} = response} -> result.(response)
      {:ok, %{status: status}} -> {:error, Errors.api(status)}
      {:error, error} -> {:error, error}
    end
  end

  defp decode(%{status: status, body: body}, module) do
    case Deserializer.json_decode(body, module) do
      {:ok, %{__struct__: ^module} = value} -> {:ok, value}
      _other -> {:error, Errors.api(status)}
    end
  end

  defp versioned(%{headers: headers} = response) do
    with {:ok, %EventSchemaVersion{} = version} <- decode(response, EventSchemaVersion) do
      {:ok, %SchemaVersionResult{schema_version: version, etag: Errors.header(headers, "etag")}}
    end
  end

  defp nothing({:ok, nil}), do: :ok
  defp nothing({:error, error}), do: {:error, error}

  defp require_action(action) when is_binary(action) and action != "", do: :ok

  defp require_action(_action) do
    {:error, Errors.validation("action", "action must be a non-empty string")}
  end

  defp definition_path(action), do: @definitions_path <> encode_segment(action) <> "/"
  defp versions_path(action), do: definition_path(action) <> "schema-versions/"
  defp version_path(action, version), do: versions_path(action) <> "#{version}/"

  # One path segment: everything but the unreserved characters is percent-encoded.
  defp encode_segment(segment), do: URI.encode(segment, &URI.char_unreserved?/1)

  # The supplied `members` of a map or keyword list, string-keyed, without nils.
  defp members(map, allowed) do
    map
    |> Enum.map(fn {key, value} -> {to_string(key), value} end)
    |> Enum.filter(fn {key, value} -> key in allowed and not is_nil(value) end)
    |> Map.new()
  end

  # The page selectors as a query keyword list in spec order (`cursor`, `limit`).
  defp page_query(params) do
    with {:ok, keyword} <- to_keyword(params) do
      case Enum.find(keyword, fn {key, _value} -> key not in @page_parameters end) do
        {key, _value} ->
          {:error, Errors.validation(to_string(key), "unknown page parameter")}

        nil ->
          {:ok,
           keyword
           |> Enum.reject(fn {_key, value} -> is_nil(value) end)
           |> Enum.sort_by(fn {key, _value} -> Atom.to_string(key) end)}
      end
    end
  end

  defp to_keyword(params) when is_list(params) or is_map(params) do
    {:ok, Enum.map(params, fn {key, value} -> {page_key(key), value} end)}
  rescue
    _error -> {:error, Errors.validation(nil, "params must be a keyword list or a map")}
  end

  defp to_keyword(_params) do
    {:error, Errors.validation(nil, "params must be a keyword list or a map")}
  end

  defp page_key(key) when is_atom(key), do: key

  defp page_key(key) when is_binary(key),
    do: Enum.find(@page_parameters, key, &(Atom.to_string(&1) == key))

  defp page_key(key), do: key

  # A lazy sequence over a paged operation; `list` answers one page. An error
  # on a later page is raised after the earlier results were yielded.
  defp stream_pages(params, list) do
    params =
      case to_keyword(params) do
        {:ok, keyword} -> Keyword.delete(keyword, :cursor)
        {:error, error} -> raise error
      end

    Stream.resource(
      fn -> nil end,
      fn
        :halt ->
          {:halt, :halt}

        cursor ->
          page_params = if cursor, do: Keyword.put(params, :cursor, cursor), else: params

          case list.(page_params) do
            {:ok, %{results: results, next_cursor: next}} ->
              {results, if(is_binary(next) and next != "", do: next, else: :halt)}

            {:error, error} ->
              raise error
          end
      end,
      fn _state -> :ok end
    )
  end
end
