defmodule Santati.Retry do
  @moduledoc false

  # The facade's retry loop. The generated core is called with retries
  # disabled, so this is the only retry logic in the SDK.

  alias Santati.{Client, RateLimitedError, ServerError, TransportError}

  @retryable_statuses [429, 500, 502, 503, 504]

  @doc """
  Runs `attempt` until it answers a response that is not retryable, or until
  the client's `max_retries` is exhausted.
  """
  @spec run(Client.t(), (-> {:ok, map()} | {:error, Exception.t()})) ::
          {:ok, map()} | {:error, Exception.t()}
  def run(%Client{} = client, attempt) when is_function(attempt, 0) do
    run(client, attempt, 1)
  end

  defp run(client, attempt, number) do
    case attempt.() do
      {:ok, %{status: status} = response} ->
        cond do
          status in 200..299 ->
            {:ok, response}

          status in @retryable_statuses ->
            retry(client, Santati.Errors.from_response(response), attempt, number)

          true ->
            {:error, Santati.Errors.from_response(response)}
        end

      {:error, error} ->
        retry(client, error, attempt, number)
    end
  end

  defp retry(client, error, attempt, number) do
    cond do
      number > client.max_retries ->
        {:error, error}

      not retryable?(error) ->
        {:error, error}

      true ->
        case backoff(client, error, number) do
          :stop ->
            {:error, error}

          milliseconds ->
            Process.sleep(milliseconds)
            run(client, attempt, number + 1)
        end
    end
  end

  defp retryable?(%TransportError{}), do: true
  defp retryable?(%ServerError{}), do: true
  defp retryable?(%RateLimitedError{code: "quota_exceeded"}), do: false
  defp retryable?(%RateLimitedError{}), do: true
  defp retryable?(_error), do: false

  defp backoff(client, %{retry_after: retry_after}, _number) when is_integer(retry_after) do
    milliseconds = retry_after * 1000

    if milliseconds > client.max_backoff_ms, do: :stop, else: milliseconds
  end

  defp backoff(client, _error, number) do
    ceiling = min(client.initial_backoff_ms * Integer.pow(2, number - 1), client.max_backoff_ms)
    :rand.uniform(ceiling + 1) - 1
  end
end
