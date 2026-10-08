defmodule Santati.SantatiError do
  @moduledoc """
  The shape every error kind of this SDK shares.

  Each of `Santati.ValidationError`, `Santati.SchemaValidationError`,
  `Santati.AuthError`, `Santati.NotFoundError`, `Santati.RateLimitedError`,
  `Santati.ServerError`, `Santati.TransportError` and `Santati.ApiError` is its
  own exception struct with the same five attributes:

    * `status` — the HTTP status, or `nil` when no response was received
    * `code` — the server's machine-readable error code, or `nil`
    * `field` — the request field the error points at, or `nil`
    * `retry_after` — the `Retry-After` response header in seconds, or `nil`
    * `message` — a human-readable message
  """

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "santati request failed"

  @type t :: %__MODULE__{
          status: non_neg_integer() | nil,
          code: String.t() | nil,
          field: String.t() | nil,
          retry_after: non_neg_integer() | nil,
          message: String.t()
        }
end

defmodule Santati.ValidationError do
  @moduledoc """
  The request was rejected before it was sent (local validation, `status` is `nil`)
  or by the server with a `400`, `413` or `422`.
  """

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "validation failed"

  @type t :: %__MODULE__{}
end

defmodule Santati.SchemaValidationError do
  @moduledoc """
  The server rejected the event against the action's JSON Schema, a disallowed
  target type, or an unusable `schema_version` pin: HTTP `400`, `413` or `422`
  with the code `schema_validation_failed`.

  An Elixir exception cannot subclass another, so this is a separate struct:
  code that matches `%Santati.ValidationError{}` does not catch it. It carries
  the same five attributes.
  """

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "schema validation failed"

  @type t :: %__MODULE__{}
end

defmodule Santati.AuthError do
  @moduledoc "The API key was rejected: HTTP `401` or `403`."

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "authentication failed"

  @type t :: %__MODULE__{}
end

defmodule Santati.NotFoundError do
  @moduledoc "The requested resource does not exist: HTTP `404`."

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "not found"

  @type t :: %__MODULE__{}
end

defmodule Santati.RateLimitedError do
  @moduledoc "The team's rate limit was exceeded: HTTP `429`."

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "rate limited"

  @type t :: %__MODULE__{}
end

defmodule Santati.ServerError do
  @moduledoc "The server failed the request: HTTP `5xx`."

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "server error"

  @type t :: %__MODULE__{}
end

defmodule Santati.TransportError do
  @moduledoc """
  No HTTP response was received: the connection was refused, the name did not
  resolve, TLS failed, or the request timed out. `status` is `nil`.
  """

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "transport error"

  @type t :: %__MODULE__{}
end

defmodule Santati.ApiError do
  @moduledoc """
  The response did not fit any other kind: an unexpected non-2xx status, an
  unexpected 2xx status, or a 2xx body that could not be decoded.
  """

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "unexpected response"

  @type t :: %__MODULE__{}
end

defmodule Santati.OutboxError do
  @moduledoc """
  The outbox store refused or failed (`outbox_full`, `store_unavailable`,
  `closed`), or a `pre_send` hook raised (`hook_failed`). `status` is `nil`.
  """

  defexception status: nil,
               code: nil,
               field: nil,
               retry_after: nil,
               message: "outbox failed"

  @type t :: %__MODULE__{}
end

defmodule Santati.Errors do
  @moduledoc false

  # Builds the error kinds out of wire level responses.

  alias Santati.{
    ApiError,
    AuthError,
    NotFoundError,
    OutboxError,
    RateLimitedError,
    SchemaValidationError,
    ServerError,
    TransportError,
    ValidationError
  }

  @spec validation(String.t() | nil, String.t()) :: ValidationError.t()
  def validation(field, message) do
    %ValidationError{status: nil, code: nil, field: field, retry_after: nil, message: message}
  end

  @spec outbox(String.t(), String.t()) :: OutboxError.t()
  def outbox(code, message) do
    %OutboxError{status: nil, code: code, field: nil, retry_after: nil, message: message}
  end

  @spec transport(term()) :: TransportError.t()
  def transport(reason) do
    %TransportError{
      status: nil,
      code: nil,
      field: nil,
      retry_after: nil,
      message: "transport error: #{inspect(reason)}"
    }
  end

  @spec api(non_neg_integer()) :: ApiError.t()
  def api(status) do
    %ApiError{status: status, code: nil, field: nil, retry_after: nil, message: "HTTP #{status}"}
  end

  @spec from_response(%{
          status: non_neg_integer(),
          headers: Tesla.Env.headers(),
          body: term()
        }) :: struct()
  def from_response(%{status: status, headers: headers, body: body}) do
    {code, field, message} = parse_body(body, status)

    kind =
      case kind(status) do
        ValidationError -> validation_kind(code)
        other -> other
      end

    struct(kind, %{
      status: status,
      code: code,
      field: field,
      retry_after: retry_after(headers),
      message: message
    })
  end

  @doc false
  # The kind of a 400, 413 or 422 whose body carried `code`.
  @spec validation_kind(String.t() | nil) :: ValidationError | SchemaValidationError
  def validation_kind("schema_validation_failed"), do: SchemaValidationError
  def validation_kind(_code), do: ValidationError

  defp kind(status) do
    cond do
      status in [400, 413, 422] -> ValidationError
      status in [401, 403] -> AuthError
      status == 404 -> NotFoundError
      status == 429 -> RateLimitedError
      status in 500..599 -> ServerError
      true -> ApiError
    end
  end

  defp parse_body(body, status) when is_binary(body) and body != "" do
    case JSON.decode(body) do
      {:ok, %{"error" => %{"code" => code} = error}} when is_binary(code) ->
        {code, string_or_nil(error["field"]), message_or_status(error["message"], status)}

      {:ok, %{"detail" => detail}} when is_binary(detail) ->
        {nil, nil, detail}

      _ ->
        {nil, nil, "HTTP #{status}"}
    end
  end

  defp parse_body(_body, status), do: {nil, nil, "HTTP #{status}"}

  defp string_or_nil(value) when is_binary(value), do: value
  defp string_or_nil(_value), do: nil

  defp message_or_status(value, _status) when is_binary(value), do: value
  defp message_or_status(_value, status), do: "HTTP #{status}"

  defp retry_after(headers) do
    case header(headers, "retry-after") do
      value when is_binary(value) ->
        if Regex.match?(~r/\A\d+\z/, value), do: String.to_integer(value), else: nil

      _value ->
        nil
    end
  end

  @doc false
  # The value of the first header named `name` (compared case-insensitively), or `nil`.
  @spec header(Tesla.Env.headers() | term(), String.t()) :: String.t() | nil
  def header(headers, name) when is_list(headers) do
    Enum.find_value(headers, fn {key, value} ->
      if key |> to_string() |> String.downcase() == name, do: value
    end)
  end

  def header(_headers, _name), do: nil
end
