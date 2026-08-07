defmodule Broadcast.GridTest do
  use ExUnit.Case, async: true

  alias Broadcast.Grid

  describe "cell/1" do
    test "positions inside one cell share it" do
      # 12 tiles per cell, 1000 milli per tile: cell 0 spans 0..11999.
      assert Grid.cell({0, 0}) == {0, 0}
      assert Grid.cell({11_999, 11_999}) == {0, 0}
    end

    test "crossing the boundary changes the cell" do
      assert Grid.cell({12_000, 0}) == {1, 0}
      assert Grid.cell({0, 12_000}) == {0, 1}
      assert Grid.cell({36_000, 24_000}) == {3, 2}
    end

    # div/2 rounds towards zero, so -1 and +1 would both land in cell 0 and two
    # cells would silently become one at each axis. Nothing puts a pip at a
    # negative coordinate today, which is exactly why this needs a test.
    test "negative positions floor rather than truncate" do
      assert Grid.cell({-1, -1}) == {-1, -1}
      assert Grid.cell({-12_000, 0}) == {-1, 0}
      assert Grid.cell({-12_001, 0}) == {-2, 0}
    end

    test "accepts a protobuf Vec2 and a missing position" do
      assert Grid.cell(%{x_milli: 12_000, y_milli: 0}) == {1, 0}
      assert Grid.cell(nil) == {0, 0}
    end
  end

  describe "topic round trip" do
    test "a topic parses back to the cell it names" do
      for cell <- [{0, 0}, {3, 2}, {-1, -4}] do
        assert {:ok, ^cell} = cell |> Grid.topic() |> Grid.parse_topic()
      end
    end

    # A client picks its own topics, so anything that is not a cell has to be
    # refused rather than becoming a subscription to a location nobody can
    # compute.
    test "rejects topics that are not cells" do
      for bad <- [
            "world:cell:1",
            "world:cell:1:2:3",
            "world:cell:a:2",
            "world:cell:1.5:2",
            "world:cell::",
            "lobby",
            "world:cell:1:2junk"
          ] do
        assert Grid.parse_topic(bad) == :error, "accepted #{inspect(bad)}"
      end
    end
  end
end
