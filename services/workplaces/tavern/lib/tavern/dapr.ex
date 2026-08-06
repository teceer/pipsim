defmodule Tavern.Dapr do
  @moduledoc """
  The slice of the Dapr HTTP API this service needs, written by hand.

  There is no Elixir SDK for Dapr, which is the whole reason this module is
  interesting: it is the check on whether "a new building type is an hour of
  work in any language" survives contact with a runtime that ships SDKs for
  five languages and not this one.

  The answer is that it does, and the bill is small. Actor state is three HTTP
  calls (`GET .../state/<key>`, `POST .../state`, and invocation), the entity
  declaration is a JSON body at a fixed path, and the callbacks are two routes
  on a `Plug` router that already existed. `:httpc` from OTP serves all of it,
  so this adds no dependency — the same argument `Tavern.Connect` makes about
  not taking a gRPC library to speak a protocol that is really just POST.

  What the SDKs would have provided and this does not: typed actor proxies,
  reminders, and timers. Only reminders would be missed here, and Dapr 1.15
  needs a separate scheduler service for those anyway.
  """

  require Logger

  @actor_type "tavern"
  @state_key "shifts"

  def actor_type, do: @actor_type
  def state_key, do: @state_key

  @doc """
  Where this pod's sidecar is, or `nil` if there is none.

  `DAPR_HTTP_PORT` is injected by the sidecar itself, so its presence is the
  signal — there is no separate flag to get out of step with reality.
  """
  @spec sidecar() :: String.t() | nil
  def sidecar do
    case System.get_env("DAPR_HTTP_PORT") do
      nil -> nil
      "" -> nil
      port -> "http://localhost:#{port}"
    end
  end

  @doc "Reads one building's state. A miss is an empty building, not an error."
  @spec get_state(String.t(), non_neg_integer()) :: {:ok, map()} | {:error, term()}
  def get_state(base, building) do
    url = "#{base}/v1.0/actors/#{@actor_type}/#{building}/state/#{@state_key}"

    case request(:get, url, nil) do
      # 204 is "this actor has never saved anything", which is the same world
      # as "nobody is working here". Telling them apart would make the first
      # claim of a building's life fail.
      {:ok, 204, _body} -> {:ok, %{}}
      {:ok, 404, _body} -> {:ok, %{}}
      {:ok, 200, ""} -> {:ok, %{}}
      {:ok, 200, body} -> decode_state(body)
      {:ok, status, body} -> {:error, {:state_get, status, body}}
      {:error, reason} -> {:error, reason}
    end
  end

  @doc "Writes one building's state through the transactional endpoint."
  @spec put_state(String.t(), non_neg_integer(), map()) :: :ok | {:error, term()}
  def put_state(base, building, shifts) do
    url = "#{base}/v1.0/actors/#{@actor_type}/#{building}/state"

    body =
      Jason.encode!([
        %{
          "operation" => "upsert",
          "request" => %{"key" => @state_key, "value" => encode_state(shifts)}
        }
      ])

    case request(:post, url, body) do
      {:ok, status, _} when status < 300 -> :ok
      {:ok, status, reply} -> {:error, {:state_post, status, reply}}
      {:error, reason} -> {:error, reason}
    end
  end

  @doc """
  Invokes a method on a building's actor, going out through the sidecar.

  This is what earns the right to write: Dapr answers the state API with
  ERR_ACTOR_INSTANCE_MISSING unless the caller is executing inside an
  invocation it routed, so the trip out and back is not indirection for its own
  sake.
  """
  @spec invoke(String.t(), non_neg_integer(), String.t(), iodata()) ::
          {:ok, binary()} | {:error, term()}
  def invoke(base, building, method, body) do
    url = "#{base}/v1.0/actors/#{@actor_type}/#{building}/method/#{method}"

    case request(:post, url, body) do
      {:ok, status, reply} when status < 300 -> {:ok, reply}
      {:ok, status, reply} -> {:error, {:invoke, method, status, reply}}
      {:error, reason} -> {:error, reason}
    end
  end

  @doc """
  What the sidecar polls at startup to learn which entities this app hosts.

  Get it wrong and actors are never registered, which then fails as "actor
  instance is missing" a long way from the cause.
  """
  def config do
    %{
      entities: [@actor_type],
      # An hour, because a building is not idle in the sense Dapr means: it
      # exists whether or not anyone is drinking in it, and deactivating one
      # only costs a reload on the next call.
      actorIdleTimeout: "1h",
      actorScanInterval: "30s",
      drainOngoingCallTimeout: "30s",
      drainRebalancedActors: true
    }
  end

  # --- wire form -------------------------------------------------------------

  # Shift structs are the tavern's; the JSON shape is this module's. Tagging the
  # struct instead would let the store's format leak into the rules.
  defp encode_state(shifts) do
    Map.new(shifts, fn {pip, s} ->
      {to_string(pip),
       %{
         "started_tick" => s.started_tick,
         "last_work_tick" => s.last_work_tick,
         "last_work_ms" => s.last_work_ms
       }}
    end)
  end

  defp decode_state(body) do
    with {:ok, raw} <- Jason.decode(body) do
      {:ok,
       Map.new(raw, fn {pip, s} ->
         {String.to_integer(pip),
          %Tavern.Shifts.Shift{
            started_tick: s["started_tick"],
            last_work_tick: s["last_work_tick"],
            last_work_ms: s["last_work_ms"]
          }}
       end)}
    end
  rescue
    e -> {:error, {:state_decode, e}}
  end

  # --- transport -------------------------------------------------------------

  # :httpc rather than a client library. Adds nothing to mix.exs, and what is
  # being spoken here is POST with a JSON body — the same argument Tavern.Connect
  # makes for not taking a gRPC stack to serve unary Connect.
  defp request(method, url, body) do
    request =
      case method do
        :get -> {String.to_charlist(url), []}
        :post -> {String.to_charlist(url), [], ~c"application/json", body || ""}
      end

    case :httpc.request(method, request, [{:timeout, 5_000}], body_format: :binary) do
      {:ok, {{_version, status, _reason}, _headers, reply}} ->
        {:ok, status, reply}

      {:error, reason} ->
        {:error, reason}
    end
  end
end

defmodule Tavern.Store.Dapr do
  @moduledoc """
  A building's shifts, in the Dapr actor state store.

  Usable only from inside an actor invocation — see `Tavern.Dapr.invoke/4` for
  why. The Connect handler therefore does not call this; it forwards, and the
  actor endpoint on the other side is what lands here.

  No compare-and-set and no script. The `GenServer` store gets its atomicity
  from the process and the farm's Redis store gets it from Lua; here the actor
  runtime allows one invocation at a time per building, so read-modify-write is
  already indivisible.
  """

  @behaviour Tavern.Store

  alias Tavern.Dapr
  alias Tavern.Shifts
  alias Tavern.ShiftSet

  require Logger

  @impl true
  def claim(building, pip, tick) do
    case load(building) do
      {:error, reason} ->
        {:error, inspect(reason)}

      {:ok, shifts} ->
        claim_in(building, shifts, pip, tick)
    end
  end

  defp claim_in(building, shifts, pip, tick) do
    case ShiftSet.claim(shifts, pip, tick, now(), Shifts.rules()) do
      {:ok, updated, expired} ->
        log_expired(expired)
        save(building, updated)

      {:error, reason, _updated, expired} ->
        # Deliberately not written back. Offers are rejected constantly once a
        # building is full, and persisting the unchanged set on each one would
        # turn a busy tavern into a write storm.
        log_expired(expired)
        {:error, reason}
    end
  end

  @impl true
  def touch(building, pip, tick) do
    with {:ok, shifts} <- load(building),
         {:ok, elapsed, updated} <- ShiftSet.touch(shifts, pip, tick, now(), Shifts.rules()) do
      # Written even when elapsed is zero: Touch is what renews the lease, and
      # skipping the write would let a shift expire while it is being worked.
      case save(building, updated) do
        :ok -> {:ok, elapsed}
        error -> error
      end
    else
      :not_on_shift -> :not_on_shift
      {:error, reason} -> {:error, inspect(reason)}
    end
  end

  @impl true
  def release(building, pip) do
    with {:ok, shifts} <- load(building),
         {:ok, started, updated} <- ShiftSet.release(shifts, pip) do
      case save(building, updated) do
        :ok -> {:ok, started}
        error -> error
      end
    else
      :not_on_shift -> :not_on_shift
      {:error, reason} -> {:error, inspect(reason)}
    end
  end

  @impl true
  def count(building) do
    case load(building) do
      {:ok, shifts} ->
        map_size(shifts)

      {:error, reason} ->
        Logger.warning("could not count shifts", building: building, error: inspect(reason))
        0
    end
  end

  defp load(building), do: Dapr.get_state(base(), building)
  defp save(building, shifts), do: Dapr.put_state(base(), building, shifts)

  defp base do
    Dapr.sidecar() || raise "Tavern.Store.Dapr used without a sidecar"
  end

  # Wall clock, not monotonic — and the difference matters here in a way it does
  # not for the in-process store.
  #
  # `Tavern.Shifts` uses monotonic time, which is right for state that dies with
  # the process. This state outlives it: a lease written before a restart is
  # compared against a reading taken after one, and monotonic time has no
  # meaning across that boundary. It would leave every shift either immortal or
  # instantly expired, depending on which way the origin moved.
  defp now, do: System.system_time(:millisecond)

  defp log_expired(pips) do
    Enum.each(pips, fn pip -> Logger.info("shift lease expired", pip: pip) end)
  end
end
