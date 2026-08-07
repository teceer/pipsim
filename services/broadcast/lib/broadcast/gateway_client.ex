defmodule Broadcast.GatewayClient do
  @moduledoc """
  One `StreamWorld` subscription per node, fanned out locally.

  This is the entire reason the service exists (ADR 0010 decision 1). Before
  it, every browser opened its own stream through the gateway into sim-core, so
  the simulation paid O(clients) for a fan-out where every client receives the
  same bytes. Here a node subscribes once and multiplies on the BEAM, which is
  the machine that is good at it. If each browser still cost sim-core a stream,
  an Elixir hop in the middle would be pure added latency.

  Reconnects with backoff: a gateway restart must not require the browsers to
  do anything. They stay connected to a node that simply has nothing new to say
  for a moment.
  """

  use GenServer
  require Logger

  alias Broadcast.{Grid, WorldService}
  alias Pips.World.V1.StreamWorldRequest

  # The node's identity to the gateway. Not a browser's — the gateway sees one
  # subscriber per broadcast node, which is the whole point.
  @client_id "broadcast"

  @initial_backoff 1_000
  @max_backoff 30_000

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  @impl true
  def init(opts) do
    url = Keyword.fetch!(opts, :url)
    {:ok, %{url: url, channel: nil, backoff: @initial_backoff}, {:continue, :connect}}
  end

  @impl true
  def handle_continue(:connect, state) do
    case connect(state.url) do
      {:ok, channel, stream} ->
        Logger.info("streaming the world", gateway: state.url)
        # Consumed in a task so a slow or dead upstream cannot block this
        # process from handling a disconnect. The stream is passed in the
        # closure rather than through the process dictionary — the task is a
        # different process, so anything put there would not be there to get.
        parent = self()
        Task.start_link(fn -> consume(stream, parent) end)
        {:noreply, %{state | channel: channel, backoff: @initial_backoff}}

      {:error, reason} ->
        Logger.warning("could not reach the gateway, retrying",
          gateway: state.url,
          reason: inspect(reason),
          in_ms: state.backoff
        )

        Process.send_after(self(), :reconnect, state.backoff)
        {:noreply, %{state | channel: nil, backoff: next_backoff(state.backoff)}}
    end
  end

  @impl true
  def handle_info(:reconnect, state), do: {:noreply, state, {:continue, :connect}}

  def handle_info({:stream_ended, reason}, state) do
    Logger.warning("world stream ended, reconnecting", reason: inspect(reason))
    if state.channel, do: GRPC.Stub.disconnect(state.channel)
    Process.send_after(self(), :reconnect, state.backoff)
    {:noreply, %{state | channel: nil, backoff: next_backoff(state.backoff)}}
  end

  def handle_info(_other, state), do: {:noreply, state}

  defp connect(url) do
    # The adapter is named explicitly because grpc defaults to Gun and this
    # project depends on Mint; leaving it implicit fails at connect time rather
    # than at compile time.
    with {:ok, channel} <- GRPC.Stub.connect(url, adapter: GRPC.Client.Adapters.Mint),
         {:ok, stream} <-
           WorldService.Stub.stream_world(channel, %StreamWorldRequest{
             client_id: @client_id,
             # 0 means "from now". The contract carries from_tick and sim-core
             # ignores it — there is no delta history anywhere, by design (ADR
             # 0010 decision 3), so asking for the past would be asking for
             # something nobody keeps.
             from_tick: 0
           }) do
      {:ok, channel, stream}
    end
  end

  defp consume(stream, parent) do
    Enum.each(stream, fn
      {:ok, %{delta: nil}} -> :ok
      {:ok, %{delta: delta}} -> publish(delta)
      {:error, reason} -> send(parent, {:stream_ended, reason})
    end)

    send(parent, {:stream_ended, :closed})
  rescue
    error -> send(parent, {:stream_ended, error})
  end

  @doc """
  Splits a delta by cell and publishes each part to its topic.

  Public because it is the whole of the fan-out logic and the suite drives it
  directly — a test that had to stand up a gRPC server to check that a pip
  lands in the right cell would be testing gRPC.

  Buildings and structures ride along with every cell rather than being
  partitioned. There are a handful of them, they change rarely, and a client
  that panned into a cell holding no building would otherwise have to be told
  separately that the farm it can see is still there. Pips are the volume, and
  pips are what the grid is for.
  """
  def publish(delta) do
    delta.pips
    |> Enum.group_by(&Grid.cell(&1.position))
    |> Enum.each(fn {cell, pips} ->
      payload = %{delta | pips: pips}

      Phoenix.PubSub.broadcast!(
        Broadcast.PubSub,
        Grid.topic(cell),
        %Phoenix.Socket.Broadcast{
          topic: Grid.topic(cell),
          event: "delta",
          # {:binary, _} is what makes Phoenix's serializer fastlane this
          # straight into a binary WebSocket frame with no JSON hop — the
          # requirement behind ADR 0010 decision 5.
          payload: {:binary, Protobuf.encode(payload)}
        }
      )
    end)
  end

  # Exponential with a ceiling. Unbounded backoff would leave a node silently
  # detached for minutes after a brief gateway restart.
  defp next_backoff(current), do: min(current * 2, @max_backoff)
end
