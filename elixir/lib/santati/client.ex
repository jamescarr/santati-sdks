defmodule Santati.Client do
  @moduledoc """
  A configured Santati client. Build one with `Santati.new/1`.
  """

  alias Santati.Errors

  @default_base_url "https://api.santati.io"
  @default_timeout_ms 10_000
  @default_max_retries 2
  @default_initial_backoff_ms 250
  @default_max_backoff_ms 8_000

  defstruct [
    :api_key,
    :base_url,
    :trail,
    :timeout_ms,
    :max_retries,
    :initial_backoff_ms,
    :max_backoff_ms,
    :headers,
    :http
  ]

  @type t :: %__MODULE__{
          api_key: String.t(),
          base_url: String.t(),
          trail: String.t() | nil,
          timeout_ms: pos_integer(),
          max_retries: non_neg_integer(),
          initial_backoff_ms: non_neg_integer(),
          max_backoff_ms: non_neg_integer(),
          headers: Enumerable.t(),
          http: Tesla.Client.t()
        }

  @doc false
  @spec new(keyword()) :: {:ok, t()} | {:error, Santati.ValidationError.t()}
  def new(options) when is_list(options) do
    api_key = Keyword.get(options, :api_key)
    headers = Keyword.get(options, :headers, %{})

    with :ok <- validate_api_key(api_key),
         :ok <- validate_headers(headers) do
      base_url = options |> Keyword.get(:base_url, @default_base_url) |> normalize_base_url()
      timeout_ms = Keyword.get(options, :timeout_ms, @default_timeout_ms)

      {:ok,
       %__MODULE__{
         api_key: api_key,
         base_url: base_url,
         trail: normalize_trail(Keyword.get(options, :trail)),
         timeout_ms: timeout_ms,
         max_retries: Keyword.get(options, :max_retries, @default_max_retries),
         initial_backoff_ms:
           Keyword.get(options, :initial_backoff_ms, @default_initial_backoff_ms),
         max_backoff_ms: Keyword.get(options, :max_backoff_ms, @default_max_backoff_ms),
         headers: headers,
         http: build_http(api_key, base_url, timeout_ms, headers)
       }}
    end
  end

  def new(_options) do
    {:error, Errors.validation(nil, "options must be a keyword list")}
  end

  @doc false
  @spec request(t(), atom(), String.t(), keyword()) ::
          {:ok, %{status: non_neg_integer(), headers: Tesla.Env.headers(), body: term()}}
          | {:error, Exception.t()}
  def request(%__MODULE__{} = client, method, path, options \\ []) do
    request = [
      method: method,
      url: path,
      query: Keyword.get(options, :query, []),
      body: Keyword.get(options, :body)
    ]

    Santati.Retry.run(client, fn ->
      case SantatiCore.Connection.request(client.http, request) do
        {:ok, %Tesla.Env{status: status, headers: headers, body: body}} ->
          {:ok, %{status: status, headers: headers, body: body}}

        # The body holds a term JSON cannot represent (a tuple, a pid, …): a
        # caller error raised like JSON.encode!/1, never a retryable transport failure.
        {:error, {Tesla.Middleware.JSON, :encode, error}} when is_exception(error) ->
          raise error

        {:error, reason} ->
          {:error, Errors.transport(reason)}
      end
    end)
  end

  defp validate_api_key(api_key) when is_binary(api_key) and api_key != "", do: :ok

  defp validate_api_key(_api_key) do
    {:error, Errors.validation("api_key", "api_key must be a non-empty string")}
  end

  defp validate_headers(headers) do
    if Enum.any?(headers, fn {name, _value} -> authorization?(name) end) do
      {:error, Errors.validation("headers", "headers must not set an authorization header")}
    else
      :ok
    end
  end

  defp authorization?(name) do
    name |> to_string() |> String.downcase() == "authorization"
  end

  defp normalize_base_url(base_url) when is_binary(base_url) do
    String.trim_trailing(base_url, "/")
  end

  defp normalize_base_url(base_url), do: base_url

  defp normalize_trail(trail) when is_binary(trail) and trail != "", do: trail
  defp normalize_trail(_trail), do: nil

  defp build_http(api_key, base_url, timeout_ms, headers) do
    middleware =
      SantatiCore.Connection.middleware(
        base_url: base_url,
        user_agent: "santati-elixir/#{Santati.version()}"
      ) ++
        [
          {Tesla.Middleware.Headers,
           [{"authorization", "Api-Key " <> api_key} | header_list(headers)]},
          {Tesla.Middleware.Timeout, timeout: timeout_ms}
        ]

    Tesla.client(middleware, {Tesla.Adapter.Mint, timeout: timeout_ms})
  end

  defp header_list(headers) when is_map(headers) or is_list(headers) do
    Enum.map(headers, fn {name, value} -> {to_string(name), to_string(value)} end)
  end

  defp header_list(_headers), do: []
end
