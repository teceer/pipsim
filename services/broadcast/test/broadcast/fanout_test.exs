defmodule Broadcast.FanoutTest do
  @moduledoc """
  The property the whole service exists for: one delta in, one message per
  cell out, and a subscriber hearing only about its own cell.

  Driven through `GatewayClient.publish/1` rather than over a real stream — a
  test that stood up a gRPC server to check which cell a pip lands in would be
  testing gRPC.
  """

  use ExUnit.Case, async: false

  alias Broadcast.{GatewayClient, Grid}
  alias Pips.Sim.V1.{PipDelta, Vec2, WorldDelta}

  # No setup starting PubSub: the application's own supervision tree already
  # runs it under `mix test`, which is itself worth knowing — the tree boots
  # without a gateway and without a listener.

  defp pip(id, x, y) do
    %PipDelta{id: id, position: %Vec2{x_milli: x, y_milli: y}}
  end

  defp delta(pips), do: %WorldDelta{tick: 7, pips: pips}

  test "a subscriber receives only the pips in its cell" do
    Phoenix.PubSub.subscribe(Broadcast.PubSub, Grid.topic({0, 0}))

    GatewayClient.publish(delta([pip(1, 500, 500), pip(2, 20_000, 500)]))

    assert_receive %Phoenix.Socket.Broadcast{event: "delta", payload: {:binary, bytes}}
    decoded = Protobuf.decode(bytes, WorldDelta)

    assert Enum.map(decoded.pips, & &1.id) == [1], "cell 0,0 heard about a pip in cell 1,0"
    assert decoded.tick == 7, "the tick has to survive the split; a client uses it to spot gaps"

    # And nothing further: the pip in the other cell must not arrive here at all.
    refute_receive %Phoenix.Socket.Broadcast{}, 50
  end

  test "each cell gets its own message" do
    Phoenix.PubSub.subscribe(Broadcast.PubSub, Grid.topic({0, 0}))
    Phoenix.PubSub.subscribe(Broadcast.PubSub, Grid.topic({1, 0}))

    GatewayClient.publish(delta([pip(1, 500, 500), pip(2, 20_000, 500)]))

    assert_receive %Phoenix.Socket.Broadcast{topic: first, payload: {:binary, _}}
    assert_receive %Phoenix.Socket.Broadcast{topic: second, payload: {:binary, _}}
    assert Enum.sort([first, second]) == Enum.sort(["world:cell:0:0", "world:cell:1:0"])
  end

  # A cell with nobody in it is not published to. Pushing an empty delta per
  # cell per tick would be most of the traffic in a sparse world.
  test "empty cells are silent" do
    Phoenix.PubSub.subscribe(Broadcast.PubSub, Grid.topic({2, 2}))

    GatewayClient.publish(delta([pip(1, 500, 500)]))

    refute_receive %Phoenix.Socket.Broadcast{}, 50
  end

  # {:binary, _} is what makes Phoenix fastlane the payload into a binary frame
  # instead of encoding it as JSON. If this ever becomes a plain map, every
  # client pays a decode and re-encode per tick.
  test "the payload stays protobuf bytes" do
    Phoenix.PubSub.subscribe(Broadcast.PubSub, Grid.topic({0, 0}))

    GatewayClient.publish(delta([pip(1, 100, 100)]))

    assert_receive %Phoenix.Socket.Broadcast{payload: payload}
    assert {:binary, bytes} = payload
    assert is_binary(bytes)
  end
end
