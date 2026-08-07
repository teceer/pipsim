defmodule Broadcast.Grid do
  @moduledoc """
  Interest management: which cell of the world a position falls in.

  ADR 0010 decision 2. One Channel topic per cell, clients joining the cells
  their viewport covers plus a ring of neighbours. This is deliberately the
  dumbest spatial partition there is — no visibility graph, no per-entity
  subscriptions — because the world is 48×30 tiles and the point is to have the
  seam in place before it matters.

  The core knows nothing about cells. Positions arrive in the delta and the
  cell is computed here, which is why growing the world later is a change to
  this module and not to `sim.proto`.
  """

  # Milli-tiles, matching sim-core's fixed point: 1000 == one tile.
  @milli_per_tile 1_000

  # Tiles per cell. Sized so a default viewport spans a handful of cells, not
  # one and not fifty — at 48×30 tiles this makes a 4×2 grid of cells.
  @tiles_per_cell 12

  @doc "Tiles along one edge of a cell."
  def tiles_per_cell, do: @tiles_per_cell

  @doc """
  The cell containing a milli-tile position, as `{cx, cy}`.

  Floor division, not truncation: `div/2` in Elixir rounds towards zero, so a
  position at -1 milli would land in cell 0 alongside +1, folding two cells
  into one at each axis. Nothing places a pip at a negative coordinate today,
  which is exactly why this would go unnoticed.
  """
  def cell({x_milli, y_milli}), do: {cell_axis(x_milli), cell_axis(y_milli)}

  def cell(%{x_milli: x, y_milli: y}), do: cell({x, y})
  def cell(nil), do: {0, 0}

  defp cell_axis(milli) do
    Integer.floor_div(milli, @milli_per_tile * @tiles_per_cell)
  end

  @doc """
  The Channel topic for a cell.

  Kept next to `cell/1` so the two can never disagree about the format — the
  client builds the same string to subscribe, and a mismatch is silence rather
  than an error.
  """
  def topic({cx, cy}), do: "world:cell:#{cx}:#{cy}"

  @doc "The topic a position belongs to."
  def topic_for(position), do: position |> cell() |> topic()

  @doc """
  Parses a topic back into a cell, for a Channel join.

  Returns `:error` for anything that is not one of ours, so a client cannot
  join `world:cell:../..` or an unbounded integer and have it treated as a
  location.
  """
  def parse_topic("world:cell:" <> rest) do
    with [cx, cy] <- String.split(rest, ":"),
         {cx, ""} <- Integer.parse(cx),
         {cy, ""} <- Integer.parse(cy) do
      {:ok, {cx, cy}}
    else
      _ -> :error
    end
  end

  def parse_topic(_), do: :error
end
