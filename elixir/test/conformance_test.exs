defmodule Santati.ConformanceTest do
  @moduledoc false

  # Runs every vector in `conformance/cases/*.json` against this SDK. One ExUnit
  # test per case, named by the case id; the mock gateway lives in
  # `Santati.ConformanceGateway`.

  use ExUnit.Case, async: false

  alias Santati.ConformanceGateway

  @cases_dir Path.expand("../../conformance/cases", __DIR__)

  files = @cases_dir |> Path.join("*.json") |> Path.wildcard() |> Enum.sort()

  if files == [] do
    raise "no conformance vectors found in #{@cases_dir}"
  end

  for file <- files do
    @external_resource file
  end

  @cases files
         |> Enum.flat_map(&JSON.decode!(File.read!(&1))["cases"])
         |> Enum.sort_by(& &1["id"])

  if @cases == [] do
    raise "the conformance vectors in #{@cases_dir} contain no cases"
  end

  @error_kinds %{
    Santati.ValidationError => "ValidationError",
    Santati.AuthError => "AuthError",
    Santati.NotFoundError => "NotFoundError",
    Santati.RateLimitedError => "RateLimitedError",
    Santati.ServerError => "ServerError",
    Santati.TransportError => "TransportError",
    Santati.ApiError => "ApiError"
  }

  for test_case <- @cases do
    test test_case["id"] do
      run_case(unquote(Macro.escape(test_case)))
    end
  end

  defp run_case(test_case) do
    input = test_case["input"]
    input_client = input["client"]
    server = start_gateway(input["gateway"])

    try do
      result =
        case Santati.new(client_options(input_client, base_url(server, input_client))) do
          {:ok, client} -> invoke(client, input, test_case["operation"])
          {:error, error} -> {:error, error}
        end

      assert_result(test_case["expect"], result, recorded(server))
    after
      stop_gateway(server)
    end
  end

  defp client_options(input_client, base_url) do
    [
      api_key: input_client["api_key"],
      base_url: base_url,
      trail: input_client["trail"],
      timeout_ms: input_client["timeout_ms"],
      max_retries: input_client["max_retries"],
      initial_backoff_ms: input_client["initial_backoff_ms"],
      max_backoff_ms: input_client["max_backoff_ms"],
      headers: input_client["headers"]
    ]
    |> Enum.reject(fn {_key, value} -> is_nil(value) end)
  end

  defp base_url(nil, input_client) do
    "http://127.0.0.1:1" <> (input_client["base_path"] || "")
  end

  defp base_url(%{origin: origin}, input_client) do
    origin <> (input_client["base_path"] || "")
  end

  defp start_gateway(%{"unreachable" => true}), do: nil
  defp start_gateway(gateway), do: ConformanceGateway.start(gateway)

  defp stop_gateway(nil), do: :ok
  defp stop_gateway(server), do: ConformanceGateway.stop(server)

  defp recorded(nil), do: []
  defp recorded(server), do: ConformanceGateway.requests(server)

  defp invoke(client, input, "emit") do
    client |> Santati.Events.emit(input["event"]) |> wire()
  end

  defp invoke(client, input, "emit_batch") do
    client |> Santati.Events.emit_batch(input["events"]) |> wire()
  end

  defp invoke(client, input, "list") do
    client |> Santati.Events.list(input["params"] || %{}) |> wire()
  end

  defp invoke(client, input, "iterate") do
    events =
      client
      |> Santati.Events.stream(input["params"] || %{})
      |> Enum.map(&to_wire/1)

    {:ok, events}
  rescue
    error -> {:error, error}
  end

  defp invoke(_client, _input, operation) do
    flunk("unknown conformance operation #{inspect(operation)}")
  end

  defp wire({:ok, value}), do: {:ok, to_wire(value)}
  defp wire({:error, error}), do: {:error, error}

  defp to_wire(value), do: value |> JSON.encode!() |> JSON.decode!()

  defp assert_result(expect, result, requests) do
    bindings =
      case {result, Map.fetch(expect, "ok"), Map.fetch(expect, "error")} do
        {{:ok, value}, {:ok, expected}, :error} ->
          match!(strip_nulls(expected), strip_nulls(value), %{})

        {{:ok, value}, :error, {:ok, expected}} ->
          flunk("expected #{expected["kind"]}, got #{inspect(value)}")

        {{:error, error}, :error, {:ok, expected}} ->
          assert_error(expected, error)
          %{}

        {{:error, error}, {:ok, _expected}, :error} ->
          flunk("expected a result, got #{inspect(error)}")
      end

    case Map.fetch(expect, "requests") do
      {:ok, expected_requests} -> assert_requests(expected_requests, requests, bindings)
      :error -> :ok
    end
  end

  defp assert_error(expected, error) when is_map(error) do
    kind = Map.get(@error_kinds, error.__struct__) || flunk("unexpected error #{inspect(error)}")
    assert_equal(expected["kind"], kind, "error kind")

    if Map.has_key?(expected, "status") do
      assert_equal(expected["status"], Map.get(error, :status), "error status")
    end

    if Map.has_key?(expected, "code") do
      assert_equal(expected["code"], Map.get(error, :code), "error code")
    end

    if Map.has_key?(expected, "field") do
      assert_equal(expected["field"], Map.get(error, :field), "error field")
    end

    if Map.has_key?(expected, "retry_after") do
      assert_equal(expected["retry_after"], Map.get(error, :retry_after), "error retry_after")
    end
  end

  defp assert_error(expected, error) do
    flunk("expected #{expected["kind"]}, got #{inspect(error)}")
  end

  defp assert_requests(expected, actual, bindings) do
    unless length(expected) == length(actual) do
      flunk("expected #{length(expected)} requests, the gateway recorded #{inspect(actual)}")
    end

    expected
    |> Enum.zip(actual)
    |> Enum.reduce(bindings, fn {expected, actual}, bindings ->
      assert_equal(expected["method"], actual["method"], "request method")
      assert_equal(expected["path"], actual["path"], "request path")
      bindings = assert_headers(expected["headers"] || %{}, actual["headers"] || %{}, bindings)

      case Map.fetch(expected, "body") do
        {:ok, body} -> match!(body, actual["body"], bindings)
        :error -> bindings
      end
    end)
  end

  defp assert_headers(expected, actual, bindings) do
    Enum.reduce(expected, bindings, fn {name, value}, bindings ->
      case Map.fetch(actual, String.downcase(name)) do
        {:ok, ^value} -> bindings
        {:ok, sent} -> flunk("header #{name}: expected #{inspect(value)}, got #{inspect(sent)}")
        :error -> flunk("header #{name} was not sent: #{inspect(actual)}")
      end
    end)
  end

  defp match!(%{"$generated" => label} = expected, actual, bindings)
       when map_size(expected) == 1 do
    bind_generated(label, actual, bindings)
  end

  defp match!(expected, actual, bindings) when is_map(expected) do
    unless is_map(actual) do
      flunk("expected an object, got #{inspect(actual)}")
    end

    expected_keys = expected |> Map.keys() |> Enum.sort()
    actual_keys = actual |> Map.keys() |> Enum.sort()

    unless expected_keys == actual_keys do
      flunk("expected keys #{inspect(expected_keys)}, got #{inspect(actual_keys)}")
    end

    Enum.reduce(expected_keys, bindings, fn key, bindings ->
      match!(Map.fetch!(expected, key), Map.fetch!(actual, key), bindings)
    end)
  end

  defp match!(expected, actual, bindings) when is_list(expected) do
    unless is_list(actual) do
      flunk("expected a list, got #{inspect(actual)}")
    end

    unless length(expected) == length(actual) do
      flunk("expected #{length(expected)} items, got #{length(actual)}")
    end

    expected
    |> Enum.zip(actual)
    |> Enum.reduce(bindings, fn {expected, actual}, bindings ->
      match!(expected, actual, bindings)
    end)
  end

  defp match!(expected, actual, bindings) do
    assert_equal(expected, actual, "value")
    bindings
  end

  defp bind_generated(label, value, bindings) do
    cond do
      not is_binary(value) or value == "" ->
        flunk("$generated #{label} must bind a non-empty string, got #{inspect(value)}")

      Map.get(bindings, label) == value ->
        bindings

      Map.has_key?(bindings, label) ->
        flunk("$generated #{label} was #{inspect(bindings[label])}, got #{inspect(value)}")

      Enum.any?(bindings, fn {_label, bound} -> bound == value end) ->
        flunk("$generated #{label} reuses another label's value #{inspect(value)}")

      true ->
        Map.put(bindings, label, value)
    end
  end

  defp assert_equal(expected, actual, what) do
    unless expected == actual do
      flunk("#{what}: expected #{inspect(expected)}, got #{inspect(actual)}")
    end
  end

  defp strip_nulls(map) when is_map(map) do
    map
    |> Enum.reject(fn {_key, value} -> is_nil(value) end)
    |> Map.new(fn {key, value} -> {key, strip_nulls(value)} end)
  end

  defp strip_nulls(list) when is_list(list), do: Enum.map(list, &strip_nulls/1)
  defp strip_nulls(value), do: value
end
