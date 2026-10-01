defmodule Santati.ConformanceGateway do
  @moduledoc false

  # The conformance suite's mock gateway. A case hands it a scripted `gateway`
  # (a Response or a `{"sequence": [...]}`) and the gateway answers every
  # request with the scripted response while recording what it saw:
  #
  #     %{"method" => ..., "path" => ..., "headers" => %{...}, "body" => parsed}
  #
  # `path` is the raw request target, query string included. `body` is the
  # parsed JSON body, or `nil` when the request carried none.

  @behaviour Plug

  @impl Plug
  def init(agent), do: agent

  @impl Plug
  def call(conn, agent) do
    {body, conn} = read_body(conn)

    request = %{
      "method" => conn.method,
      "path" => request_path(conn),
      "headers" => Map.new(conn.req_headers),
      "body" => body
    }

    response =
      Agent.get_and_update(agent, fn state ->
        {response(state.gateway, length(state.requests)),
         %{state | requests: state.requests ++ [request]}}
      end)

    if delay = response["delay_ms"], do: Process.sleep(delay)

    respond(conn, response)
  end

  @doc "Starts a gateway on an ephemeral loopback port."
  @spec start(map()) :: %{agent: pid(), server: pid(), origin: String.t()}
  def start(gateway) do
    {:ok, agent} = Agent.start_link(fn -> %{gateway: gateway, requests: []} end)

    {:ok, server} =
      Bandit.start_link(
        plug: {__MODULE__, agent},
        ip: :loopback,
        port: 0,
        startup_log: false
      )

    {:ok, {_address, port}} = ThousandIsland.listener_info(server)

    %{agent: agent, server: server, origin: "http://127.0.0.1:#{port}"}
  end

  @doc "Stops a gateway and the agent holding its state."
  @spec stop(%{agent: pid(), server: pid(), origin: String.t()}) :: :ok
  def stop(%{agent: agent, server: server}) do
    ThousandIsland.stop(server)
    if Process.alive?(agent), do: Agent.stop(agent)
    :ok
  end

  @doc "Every request recorded so far, oldest first."
  @spec requests(%{agent: pid()}) :: [map()]
  def requests(%{agent: agent}), do: Agent.get(agent, & &1.requests)

  defp request_path(conn) do
    case conn.query_string do
      "" -> conn.request_path
      query -> conn.request_path <> "?" <> query
    end
  end

  defp read_body(conn) do
    case Plug.Conn.read_body(conn) do
      {:ok, "", conn} -> {nil, conn}
      {:ok, body, conn} -> {decode(body), conn}
      {:more, _body, conn} -> {nil, conn}
      {:error, _reason} -> {nil, conn}
    end
  end

  defp decode(body) do
    case JSON.decode(body) do
      {:ok, decoded} -> decoded
      _error -> nil
    end
  end

  defp response(%{"sequence" => [first | _rest] = sequence}, index) do
    Enum.at(sequence, min(index, length(sequence) - 1)) || first
  end

  defp response(%{"status" => _status} = response, _index), do: response

  defp respond(conn, response) do
    {content_type, body} = encode_body(response["body"])

    conn =
      response
      |> Map.get("headers", %{})
      |> Enum.reduce(conn, fn {name, value}, conn ->
        Plug.Conn.put_resp_header(conn, name, to_string(value))
      end)

    conn =
      if content_type && Plug.Conn.get_resp_header(conn, "content-type") == [] do
        Plug.Conn.put_resp_header(conn, "content-type", content_type)
      else
        conn
      end

    Plug.Conn.send_resp(conn, response["status"], body)
  end

  defp encode_body(nil), do: {nil, ""}
  defp encode_body(%{"json" => json}), do: {"application/json", JSON.encode!(json)}
  defp encode_body(%{"text" => text}), do: {"text/plain; charset=utf-8", text}
  defp encode_body(body), do: {"application/json", JSON.encode!(body)}
end
